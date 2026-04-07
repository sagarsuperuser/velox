package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

// Logger appends entries to the audit log. It's designed to be called
// fire-and-forget from service methods — audit failures should never
// block business operations.
type Logger struct {
	db *postgres.DB
}

func NewLogger(db *postgres.DB) *Logger {
	return &Logger{db: db}
}

// Log records an audit entry. Call this from service methods after a
// successful mutation. Uses a background context so it doesn't block.
func (l *Logger) Log(ctx context.Context, tenantID, action, resourceType, resourceID string, metadata map[string]any) {
	go l.write(tenantID, auth.KeyID(ctx), action, resourceType, resourceID, metadata)
}

func (l *Logger) write(tenantID, actorID, action, resourceType, resourceID string, metadata map[string]any) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	tx, err := l.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return // Silently fail — audit should never block
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

	tx.ExecContext(ctx, `
		INSERT INTO audit_log (id, tenant_id, actor_type, actor_id, action,
			resource_type, resource_id, metadata, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
	`, id, tenantID, actorType, actorID, action, resourceType, resourceID, metaJSON, time.Now().UTC())

	tx.Commit()
}

// Query reads audit entries for a tenant.
func (l *Logger) Query(ctx context.Context, tenantID string, filter QueryFilter) ([]domain.AuditEntry, error) {
	tx, err := l.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	query := `SELECT id, tenant_id, actor_type, actor_id, action, resource_type,
		resource_id, metadata, COALESCE(ip_address,''), created_at
		FROM audit_log`
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

	if where != "" {
		query += " WHERE " + where[5:] // Remove leading " AND "
	}

	query += " ORDER BY created_at DESC"
	query += fmt.Sprintf(" LIMIT $%d OFFSET $%d", idx, idx+1)
	args = append(args, limit, filter.Offset)

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []domain.AuditEntry
	for rows.Next() {
		var e domain.AuditEntry
		var metaJSON []byte
		if err := rows.Scan(&e.ID, &e.TenantID, &e.ActorType, &e.ActorID,
			&e.Action, &e.ResourceType, &e.ResourceID,
			&metaJSON, &e.IPAddress, &e.CreatedAt); err != nil {
			return nil, err
		}
		json.Unmarshal(metaJSON, &e.Metadata)
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

type QueryFilter struct {
	ResourceType string
	ResourceID   string
	Action       string
	Limit        int
	Offset       int
}

func andClause(idx *int, col string, args *[]any, val string) string {
	clause := fmt.Sprintf(" AND %s = $%d", col, *idx)
	*args = append(*args, val)
	*idx++
	return clause
}
