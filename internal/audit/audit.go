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
//
// resourceLabel is the human-readable name the /audit-logs page
// renders ("Updated arrears-mon" instead of the generic "Updated
// subscription"). Each caller passes the appropriate field — typically
// sub.Code, invoice.InvoiceNumber, customer.DisplayName, plan.Code,
// coupon.Code, etc. Pass "" when no label is available (event happens
// before the resource is hydratable, or the resource has no operator-
// facing name); the page falls back to the resource_type.
func (l *Logger) Log(ctx context.Context, tenantID, action, resourceType, resourceID, resourceLabel string, metadata map[string]any) error {
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

	// Resolve actor: a stamped customer actor (auth.WithCustomerActor)
	// takes precedence and maps to actor_type='customer'; otherwise an
	// API-key request is actor_type='api_key', falling back to 'system'
	// for background workers + cron paths that have neither.
	actorType := "api_key"
	if custID := auth.CustomerActorID(ctx); custID != "" {
		actorType = "customer"
		actorID = custID
	} else if actorID == "" {
		actorType = "system"
		actorID = "system"
	}

	ipAddress := ClientIP(ctx)
	requestID := chimw.GetReqID(ctx)

	// created_at is wall-clock — the audit row answers "when did the
	// operator click" / "when did the engine emit," which are both real-
	// time events regardless of which test clock the affected entity is
	// pinned to. ADR-030 table line 131 ("Audit log row recorded_at:
	// wall-clock | Forensics; never on the public API"). Diverged from
	// this for ~2 weeks via subscription.auditCtxForSub (since reverted
	// 2026-05-28) — see ADR-030 amendment.
	//
	// Callers who care about the *simulated* effective time of an action
	// on a clock-pinned entity pass it explicitly in metadata as
	// `sim_effective_at` + `test_clock_id`; the audit UI renders that
	// subline below the wall-clock primary timestamp.
	_, err = tx.ExecContext(writeCtx, `
		INSERT INTO audit_log (id, tenant_id, actor_type, actor_id, action,
			resource_type, resource_id, resource_label, metadata, ip_address,
			request_id, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
	`, id, tenantID, actorType, actorID, action, resourceType, resourceID, resourceLabel, metaJSON,
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

	// Default 50, clamp to 100 — was silently falling back to 50 on
	// >100 asks (no-silent-fallbacks principle, 2026-05-28).
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	} else if limit > 100 {
		limit = 100
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
	// DateFrom / DateTo arrive as time.Time (parsed at the handler via
	// timefilter.ParseRange — accepts both RFC3339 and YYYY-MM-DD).
	// Postgres-go driver handles TIMESTAMPTZ binding from time.Time.
	// See ADR-010 for the tenant-TZ model.
	if !filter.DateFrom.IsZero() {
		where += fmt.Sprintf(" AND al.created_at >= $%d", idx)
		args = append(args, filter.DateFrom)
		idx++
	}
	if !filter.DateTo.IsZero() {
		where += fmt.Sprintf(" AND al.created_at <= $%d", idx)
		args = append(args, filter.DateTo)
		idx++
	}

	useCursor := !filter.AfterCreatedAt.IsZero() && filter.AfterID != ""
	if useCursor {
		where += fmt.Sprintf(" AND (al.created_at, al.id) < ($%d, $%d)", idx, idx+1)
		args = append(args, filter.AfterCreatedAt, filter.AfterID)
		idx += 2
	}

	whereClause := ""
	if where != "" {
		whereClause = " WHERE " + where[5:] // Remove leading " AND "
	}

	// Cursor path skips COUNT — handler derives hasMore from
	// limit+1 over-fetch. Offset path keeps the count for the
	// legacy "Page X of N" UX in the dashboard.
	var total int
	if !useCursor {
		countQuery := `SELECT COUNT(*) FROM audit_log al` + whereClause
		if err := tx.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
			return nil, 0, err
		}
	}

	// LEFT JOIN api_keys so the UI can show a human-readable key name instead
	// of the raw actor_id (e.g., "Production" vs "vlx_secret_live_abc123…").
	// Join is (tenant_id, id) which matches the api_keys PK's tenant scope.
	//
	// Also LEFT JOIN customers for actor_type='customer' rows (customer-
	// portal-driven mutations). Picks display_name so the AuditLog page
	// shows "Acme Corp" instead of the generic "Customer" fallback.
	// COALESCE prefers the api-key name (when the row is api_key-actored)
	// over the customer display name (the joins are mutually exclusive in
	// practice — actor_id can't be both a key and a customer).
	query := `SELECT al.id, al.tenant_id, al.actor_type, al.actor_id,
		COALESCE(NULLIF(k.name, ''), c.display_name, '') AS actor_name,
		al.action, al.resource_type, al.resource_id,
		COALESCE(al.resource_label,''), al.metadata,
		COALESCE(al.ip_address,''), COALESCE(al.request_id,''), al.created_at
		FROM audit_log al
		LEFT JOIN api_keys k ON k.id = al.actor_id AND k.tenant_id = al.tenant_id
		LEFT JOIN customers c ON c.id = al.actor_id AND c.tenant_id = al.tenant_id AND al.actor_type = 'customer'` + whereClause

	// Order by (created_at, id) DESC — id tiebreaker aligns with the
	// cursor predicate's tuple ordering so seek + ORDER BY stay in
	// lockstep regardless of microsecond-level ties.
	query += " ORDER BY al.created_at DESC, al.id DESC"
	queryLimit := limit
	if useCursor {
		queryLimit = limit + 1
	}
	args = append(args, queryLimit)
	query += fmt.Sprintf(" LIMIT $%d", idx)
	idx++
	if !useCursor {
		args = append(args, filter.Offset)
		query += fmt.Sprintf(" OFFSET $%d", idx)
	}

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
	// DateFrom / DateTo are parsed at the handler via timefilter.ParseRange
	// — accepts both RFC3339 and bare YYYY-MM-DD. Zero-value time.Time
	// means "no filter on this end."
	DateFrom time.Time
	DateTo   time.Time
	Limit    int
	Offset   int
	// Cursor-based pagination (2026-05-29). Seek-method query:
	// WHERE (al.created_at, al.id) < (AfterCreatedAt, AfterID).
	// Mutually exclusive with Offset — handler routes by ?after=
	// query param taking precedence. Audit_log is high-write, and
	// operators paginate deep for forensics, so OFFSET 50000 got
	// slow fast. Cursor-method is O(log N) per page regardless of
	// depth.
	AfterCreatedAt time.Time
	AfterID        string
}

func andClause(idx *int, col string, args *[]any, val string) string {
	clause := fmt.Sprintf(" AND %s = $%d", col, *idx)
	*args = append(*args, val)
	*idx++
	return clause
}
