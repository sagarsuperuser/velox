package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

var auditFailures prometheus.Counter

func init() {
	c := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "velox_audit_failures_total",
		Help: "Total failed audit log writes.",
	})
	if err := prometheus.DefaultRegisterer.Register(c); err != nil {
		if are, ok := err.(prometheus.AlreadyRegisteredError); ok {
			auditFailures = are.ExistingCollector.(prometheus.Counter)
		} else {
			panic(err)
		}
	} else {
		auditFailures = c
	}
}

// Logger appends entries to the audit log. Writes are synchronous so
// callers know whether the entry was persisted — important for compliance.
type Logger struct {
	db *postgres.DB
}

func NewLogger(db *postgres.DB) *Logger {
	return &Logger{db: db}
}

// Log records an audit entry synchronously and returns an error if the
// write fails. Callers may ignore the error when audit failure should
// not block the business operation, but failures are always logged and
// counted in metrics.
func (l *Logger) Log(ctx context.Context, tenantID, action, resourceType, resourceID string, metadata map[string]any) error {
	label, _ := metadata["resource_label"].(string)
	actorID := auth.KeyID(ctx)

	writeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	tx, err := l.db.BeginTx(writeCtx, postgres.TxTenant, tenantID)
	if err != nil {
		auditFailures.Inc()
		slog.Error("audit: failed to begin transaction",
			"tenant_id", tenantID, "action", action,
			"resource_type", resourceType, "resource_id", resourceID,
			"error", err)
		return fmt.Errorf("audit log: begin tx: %w", err)
	}
	defer postgres.Rollback(tx)

	id := postgres.NewID("vlx_aud")
	metaJSON, _ := json.Marshal(metadata)
	if metadata == nil {
		metaJSON = []byte("{}")
	}

	actorType := "api_key"
	if actorID == "" {
		actorType = "system"
		actorID = "system"
	}

	_, err = tx.ExecContext(writeCtx, `
		INSERT INTO audit_log (id, tenant_id, actor_type, actor_id, action,
			resource_type, resource_id, resource_label, metadata, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
	`, id, tenantID, actorType, actorID, action, resourceType, resourceID, label, metaJSON, time.Now().UTC())
	if err != nil {
		auditFailures.Inc()
		slog.Error("audit: failed to insert entry",
			"tenant_id", tenantID, "action", action,
			"resource_type", resourceType, "resource_id", resourceID,
			"error", err)
		return fmt.Errorf("audit log: insert: %w", err)
	}

	if err := tx.Commit(); err != nil {
		auditFailures.Inc()
		slog.Error("audit: failed to commit entry",
			"tenant_id", tenantID, "action", action,
			"resource_type", resourceType, "resource_id", resourceID,
			"error", err)
		return fmt.Errorf("audit log: commit: %w", err)
	}

	return nil
}

// Query reads audit entries for a tenant.
func (l *Logger) Query(ctx context.Context, tenantID string, filter QueryFilter) ([]domain.AuditEntry, int, error) {
	tx, err := l.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return nil, 0, err
	}
	defer postgres.Rollback(tx)

	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	args := []any{}
	idx := 1
	where := ""

	if filter.ResourceType != "" {
		where += andClause(&idx, "resource_type", &args, filter.ResourceType)
	}
	if filter.ResourceID != "" {
		where += andClause(&idx, "resource_id", &args, filter.ResourceID)
	}
	if filter.Action != "" {
		where += andClause(&idx, "action", &args, filter.Action)
	}
	if filter.ActorID != "" {
		where += andClause(&idx, "actor_id", &args, filter.ActorID)
	}
	if filter.DateFrom != "" {
		where += fmt.Sprintf(" AND created_at >= $%d", idx)
		args = append(args, filter.DateFrom+"T00:00:00Z")
		idx++
	}
	if filter.DateTo != "" {
		where += fmt.Sprintf(" AND created_at <= $%d", idx)
		args = append(args, filter.DateTo+"T23:59:59Z")
		idx++
	}

	whereClause := ""
	if where != "" {
		whereClause = " WHERE " + where[5:] // Remove leading " AND "
	}

	var total int
	countQuery := `SELECT COUNT(*) FROM audit_log` + whereClause
	if err := tx.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	query := `SELECT id, tenant_id, actor_type, actor_id, action, resource_type,
		resource_id, COALESCE(resource_label,''), metadata, COALESCE(ip_address,''), created_at
		FROM audit_log` + whereClause

	query += " ORDER BY created_at DESC"
	query += fmt.Sprintf(" LIMIT $%d OFFSET $%d", idx, idx+1)
	args = append(args, limit, filter.Offset)

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var entries []domain.AuditEntry
	for rows.Next() {
		var e domain.AuditEntry
		var metaJSON []byte
		if err := rows.Scan(&e.ID, &e.TenantID, &e.ActorType, &e.ActorID,
			&e.Action, &e.ResourceType, &e.ResourceID, &e.ResourceLabel,
			&metaJSON, &e.IPAddress, &e.CreatedAt); err != nil {
			return nil, 0, err
		}
		json.Unmarshal(metaJSON, &e.Metadata)
		entries = append(entries, e)
	}
	return entries, total, rows.Err()
}

type QueryFilter struct {
	ResourceType string
	ResourceID   string
	Action       string
	ActorID      string
	DateFrom     string // YYYY-MM-DD
	DateTo       string // YYYY-MM-DD
	Limit        int
	Offset       int
}

func andClause(idx *int, col string, args *[]any, val string) string {
	clause := fmt.Sprintf(" AND %s = $%d", col, *idx)
	*args = append(*args, val)
	*idx++
	return clause
}
