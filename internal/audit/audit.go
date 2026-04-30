package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

var auditWriteErrors *prometheus.CounterVec

func init() {
	c := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "velox_audit_write_errors_total",
		Help: "Total failed audit log writes, labeled by tenant.",
	}, []string{"tenant_id"})
	if err := prometheus.DefaultRegisterer.Register(c); err != nil {
		if are, ok := err.(prometheus.AlreadyRegisteredError); ok {
			auditWriteErrors = are.ExistingCollector.(*prometheus.CounterVec)
		} else {
			panic(err)
		}
	} else {
		auditWriteErrors = c
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
		auditWriteErrors.WithLabelValues(tenantID).Inc()
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

	ipAddress := ClientIP(ctx)
	requestID := chimw.GetReqID(ctx)

	_, err = tx.ExecContext(writeCtx, `
		INSERT INTO audit_log (id, tenant_id, actor_type, actor_id, action,
			resource_type, resource_id, resource_label, metadata, ip_address,
			request_id, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
	`, id, tenantID, actorType, actorID, action, resourceType, resourceID, label, metaJSON,
		nullIfEmpty(ipAddress), nullIfEmpty(requestID), time.Now().UTC())
	if err != nil {
		auditWriteErrors.WithLabelValues(tenantID).Inc()
		slog.Error("audit: failed to insert entry",
			"tenant_id", tenantID, "action", action,
			"resource_type", resourceType, "resource_id", resourceID,
			"error", err)
		return fmt.Errorf("audit log: insert: %w", err)
	}

	if err := tx.Commit(); err != nil {
		auditWriteErrors.WithLabelValues(tenantID).Inc()
		slog.Error("audit: failed to commit entry",
			"tenant_id", tenantID, "action", action,
			"resource_type", resourceType, "resource_id", resourceID,
			"error", err)
		return fmt.Errorf("audit log: commit: %w", err)
	}

	// Signal the AuditLog middleware (if this request is under it) that an
	// audit row has been written so its catch-all path suppresses a duplicate.
	MarkHandled(ctx)
	return nil
}

// nullIfEmpty preserves SQL NULL for optional text columns — avoids a
// column full of empty strings that later force callers to COALESCE.
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
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
		where += andClause(&idx, "al.resource_type", &args, filter.ResourceType)
	}
	if filter.ResourceID != "" {
		where += andClause(&idx, "al.resource_id", &args, filter.ResourceID)
	}
	if filter.Action != "" {
		where += andClause(&idx, "al.action", &args, filter.Action)
	}
	if filter.ActorID != "" {
		where += andClause(&idx, "al.actor_id", &args, filter.ActorID)
	}
	// Accept either a full RFC3339 instant (preferred — the dashboard
	// sends start/end-of-day in tenant TZ as UTC ISO) or a bare
	// yyyy-mm-dd date (legacy — interpreted as UTC midnight). The
	// dashboard moved to ISO instants in commit b523c71 and onward;
	// the date-only branch stays for any direct curl users that
	// haven't migrated. See ADR-010 for the tenant-TZ model.
	if filter.DateFrom != "" {
		where += fmt.Sprintf(" AND al.created_at >= $%d", idx)
		args = append(args, normalizeDateFilter(filter.DateFrom, false))
		idx++
	}
	if filter.DateTo != "" {
		where += fmt.Sprintf(" AND al.created_at <= $%d", idx)
		args = append(args, normalizeDateFilter(filter.DateTo, true))
		idx++
	}

	whereClause := ""
	if where != "" {
		whereClause = " WHERE " + where[5:] // Remove leading " AND "
	}

	var total int
	countQuery := `SELECT COUNT(*) FROM audit_log al` + whereClause
	if err := tx.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	// LEFT JOIN api_keys so the UI can show a human-readable key name instead
	// of the raw actor_id (e.g., "Production" vs "vlx_secret_live_abc123…").
	// Join is (tenant_id, id) which matches the api_keys PK's tenant scope.
	query := `SELECT al.id, al.tenant_id, al.actor_type, al.actor_id,
		COALESCE(k.name, '') AS actor_name,
		al.action, al.resource_type, al.resource_id,
		COALESCE(al.resource_label,''), al.metadata,
		COALESCE(al.ip_address,''), COALESCE(al.request_id,''), al.created_at
		FROM audit_log al
		LEFT JOIN api_keys k ON k.id = al.actor_id AND k.tenant_id = al.tenant_id` + whereClause

	query += " ORDER BY al.created_at DESC"
	query += fmt.Sprintf(" LIMIT $%d OFFSET $%d", idx, idx+1)
	args = append(args, limit, filter.Offset)

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()

	var entries []domain.AuditEntry
	for rows.Next() {
		var e domain.AuditEntry
		var metaJSON []byte
		if err := rows.Scan(&e.ID, &e.TenantID, &e.ActorType, &e.ActorID, &e.ActorName,
			&e.Action, &e.ResourceType, &e.ResourceID, &e.ResourceLabel,
			&metaJSON, &e.IPAddress, &e.RequestID, &e.CreatedAt); err != nil {
			return nil, 0, err
		}
		_ = json.Unmarshal(metaJSON, &e.Metadata)
		entries = append(entries, e)
	}
	return entries, total, rows.Err()
}

// FilterOptions returns the distinct actions and resource_types recorded for
// a tenant. The audit page uses this to build filter dropdowns dynamically,
// so new event types appear automatically without a UI release.
func (l *Logger) FilterOptions(ctx context.Context, tenantID string) (actions, resourceTypes []string, err error) {
	tx, err := l.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return nil, nil, err
	}
	defer postgres.Rollback(tx)

	// Cap at 500 to bound payload — a tenant shouldn't organically produce
	// more distinct actions than that; if they do, the cap bias is toward
	// the most recent rows.
	rows, err := tx.QueryContext(ctx,
		`SELECT DISTINCT action FROM audit_log ORDER BY action LIMIT 500`)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			_ = rows.Close()
			return nil, nil, err
		}
		actions = append(actions, s)
	}
	_ = rows.Close()

	rows, err = tx.QueryContext(ctx,
		`SELECT DISTINCT resource_type FROM audit_log ORDER BY resource_type LIMIT 500`)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			_ = rows.Close()
			return nil, nil, err
		}
		resourceTypes = append(resourceTypes, s)
	}
	_ = rows.Close()
	return actions, resourceTypes, nil
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

// normalizeDateFilter accepts either an RFC3339 instant
// (e.g. "2026-05-04T18:30:00Z" — what the dashboard sends, derived
// from tenant TZ start/end-of-day) or a bare yyyy-mm-dd date string
// (legacy — interpreted as UTC midnight). Returns a string suitable
// for a TIMESTAMPTZ comparison. endOfDay=true uses 23:59:59 instead
// of 00:00:00 for the date-only branch so date_to filters
// inclusively. See ADR-010.
func normalizeDateFilter(val string, endOfDay bool) string {
	if _, err := time.Parse(time.RFC3339, val); err == nil {
		return val
	}
	if endOfDay {
		return val + "T23:59:59Z"
	}
	return val + "T00:00:00Z"
}
