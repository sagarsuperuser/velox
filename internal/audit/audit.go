package audit

import (
	"context"
	"database/sql"
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
	// Detach from the caller's cancellation (keeping its values — actor,
	// client IP, request id still resolve) so a client disconnect right
	// after the business operation commits cannot abort the audit write.
	// This is the residual own-tx path (ADR-090): it carries a real
	// post-commit window, which is exactly why LogInTx is the destination for
	// every writer that owns a transaction.
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
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

	actorType, actorID := ResolveActor(ctx)

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

	// This request produced audit evidence — the AuditCoverage detector must not
	// report it as an uncovered mutation. Marked only after the COMMIT.
	markEmitted(ctx)
	return nil
}

// Entry is the typed input to LogInTx. Action/ResourceType take the
// domain.AuditAction* / existing wire strings — those strings are FROZEN
// (ADR-090): the FE filter vocabulary, badge maps, and existing rows all key
// on them, so a rename is a coordinated FE+MANUAL_TEST change, never a drive-by.
type Entry struct {
	Action        string
	ResourceType  string
	ResourceID    string
	ResourceLabel string
	Metadata      map[string]any
	// Sim carries the simulated effect-time context for actions on
	// clock-pinned entities (ADR-030 amendment; ADR-086 sim-axis). When
	// set, it is written to the sim_effective_at / test_clock_id columns
	// (migration 0148) AND mirrored into Metadata under the legacy keys the
	// dashboard already renders — the columns become queryable once
	// stamping reaches parity across writers (PR7 of the ADR-090 arc).
	Sim *SimContext
}

// SimContext identifies the test clock and simulated instant an audited
// action took effect at. Wall-clock created_at remains the primary
// timestamp (ADR-030); this is the second axis.
type SimContext struct {
	EffectiveAt time.Time
	TestClockID string
}

// LogInTx writes an audit row on the CALLER's transaction — the business
// mutation and its audit row commit or roll back together (ADR-090 in-tx
// emission, shared fate). This is what makes "mutation committed but audit
// row missing" unrepresentable on covered paths; callers must propagate the
// error (which aborts their tx), never discard it.
//
// LogInTx is TOTAL by construction (design-panel amendment A2): tenant_id is
// taken from the transaction's own app.tenant_id GUC — it cannot disagree
// with RLS, and a missing GUC fails loudly: NULLIF(…, ”) maps BOTH the
// virgin-connection NULL and the pooled-connection empty string (a reverted
// is_local set_config placeholder — the dominant case in a warm pool) onto
// the NOT NULL constraint, so the guard does not silently depend on the
// tenants FK. A metadata bag that cannot marshal degrades to a
// {"marshal_error": …} payload instead of aborting a money transaction on a
// telemetry bug; a zero-valued/partial SimContext is treated as absent
// rather than polluting the partial clock index with empty strings;
// livemode is stamped by the table trigger from the same tx session. The
// vocabulary round-trip integration test INSERTs every declared action
// constant so a value the schema rejects cannot ship.
//
// LogInTx marks the request as having produced audit evidence (ADR-090 §4,
// amended by the uninstall PR — the ADR's "LogInTx does NOT mark the request
// handled" rule was written for the catch-all WRITER, where suppression cost a
// route its only row; it does not carry to a pure OBSERVER):
//
//   - Rolled back after we return? Then the mutation failed and the handler
//     answers non-2xx, and the detector only inspects 2xx — so a rolled-back
//     emission can never make an uncovered mutation look covered.
//   - A secondary emission deep inside another route's flow (the
//     proration-fallback grant inside a change-plan request) marks that request
//     too. That costs nothing REAL — the detector is a runtime backstop, and the
//     thing that actually holds a route to its own emission is the route-audit
//     registry + its arch test (internal/api/audit_routes.go), which forces every
//     mutating route to declare explicit or exempt(reason) in review. The blind
//     spot is bounded and named: a route that emits nothing of its own but nests
//     someone else's emission reads as covered at runtime.
//
// Self-marking is also load-bearing: POST /v1/tenants and both public checkout
// routes emit ONLY in-tx, and nothing else would account for them.
func (l *Logger) LogInTx(ctx context.Context, tx *sql.Tx, e Entry) error {
	// Treat a partial/zero SimContext as absent — a half-set sim axis
	// ('' clock id, zero time) is worse than none: it lands inside the
	// partial index and defeats IS NOT NULL scoping.
	if e.Sim != nil && (e.Sim.TestClockID == "" || e.Sim.EffectiveAt.IsZero()) {
		e.Sim = nil
	}
	metadata := e.Metadata
	if e.Sim != nil {
		// Mirror into the legacy metadata keys the dashboard renders today;
		// copy so the caller's map isn't mutated.
		m := make(map[string]any, len(metadata)+2)
		for k, v := range metadata {
			m[k] = v
		}
		m["sim_effective_at"] = e.Sim.EffectiveAt.UTC().Format(time.RFC3339)
		m["test_clock_id"] = e.Sim.TestClockID
		metadata = m
	}
	metaJSON, err := json.Marshal(metadata)
	if err != nil {
		// Totality: never let an unmarshalable telemetry bag abort the
		// business tx (a nil metaJSON would violate the JSONB NOT NULL and
		// do exactly that). The row still lands, carrying the failure it
		// represents.
		metaJSON, _ = json.Marshal(map[string]string{"marshal_error": err.Error()})
	}
	if metadata == nil {
		metaJSON = []byte("{}")
	}

	actorType, actorID := ResolveActor(ctx)

	var simAt, clockID any
	if e.Sim != nil {
		simAt = e.Sim.EffectiveAt.UTC()
		clockID = e.Sim.TestClockID
	}

	// created_at stays wall-clock (ADR-030); tenant_id comes from the tx
	// GUC so the row is authoritative for the transaction it rides in.
	// NULLIF folds the pooled-connection empty-string placeholder into
	// NULL so the NOT NULL constraint — not the incidental tenants FK —
	// is what rejects a GUC-less transaction.
	_, err = tx.ExecContext(ctx, `
		INSERT INTO audit_log (id, tenant_id, actor_type, actor_id, action,
			resource_type, resource_id, resource_label, metadata, ip_address,
			request_id, sim_effective_at, test_clock_id, created_at)
		VALUES ($1, NULLIF(current_setting('app.tenant_id', true), ''), $2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
	`, postgres.NewID("vlx_aud"), actorType, actorID, e.Action, e.ResourceType,
		e.ResourceID, e.ResourceLabel, metaJSON,
		nullIfEmpty(ClientIP(ctx)), nullIfEmpty(chimw.GetReqID(ctx)),
		simAt, clockID, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("audit log (in-tx): insert: %w", err)
	}

	// This request produced audit evidence. Marked only AFTER a successful
	// INSERT: a failed emission must leave the request unaccounted-for, so the
	// caller's tx aborts (shared fate) and — if some caller were to swallow that
	// and still answer 2xx — the detector reports it rather than trusting a row
	// that isn't there.
	markEmitted(ctx)
	return nil
}

// ResolveActor determines who performed an audited action from the request
// context, in priority order:
//   - customer — a stamped customer actor (public token flows, auth.WithCustomerActor)
//   - user     — a dashboard operator on a session cookie (auth.WithUserID, ADR-011)
//   - api_key  — an SDK / curl caller on a Bearer vlx_… key (auth.WithKeyID)
//   - system   — background workers / cron with no request identity
//
// Both audit write paths (Logger.Log and the AuditLog middleware) call this so
// the actor resolves identically. Before this, both read only auth.KeyID — and
// session.applyToCtx sets WithUserID but never WithKeyID — so every dashboard
// (session-cookie) operator action recorded actor_type='system'/'system', and
// the log could not answer "who did this?" for the primary UI. The 'user'
// value the audit_log CHECK constraint reserves was never written.
func ResolveActor(ctx context.Context) (actorType, actorID string) {
	if custID := auth.CustomerActorID(ctx); custID != "" {
		return "customer", custID
	}
	if userID := auth.UserID(ctx); userID != "" {
		return "user", userID
	}
	if keyID := auth.KeyID(ctx); keyID != "" {
		return "api_key", keyID
	}
	return "system", "system"
}

// nullIfEmpty preserves SQL NULL for optional text columns — avoids a
// column full of empty strings that later force callers to COALESCE.
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// buildListWhere assembles the WHERE clause + args shared by the list and
// COUNT queries. Split out as a pure function so tests can pin the explicit
// tenant + livemode predicates: RLS (TxTenant) already enforces both, but the
// tenant-isolation policy carries a column-free bypass-GUC OR-arm, so the
// planner can never derive index quals from RLS alone — without the explicit
// predicates, every audit read (COUNT, page fetch, cursor seek) was a full
// seq scan across ALL tenants' rows (audit e2e 2026-07-13, F3). The values
// mirror exactly what BeginTx stamps into the GUCs from the caller's ctx, so
// they can only ever narrow, never disagree; RLS stays as the isolation
// backstop. Removing them keeps results identical (RLS masks it) and only
// destroys the query plan — which is why TestBuildListWhere_PinsPredicates
// exists.
func buildListWhere(tenantID string, livemode bool, filter QueryFilter) (whereClause string, args []any, nextIdx int, useCursor bool) {
	args = []any{tenantID, livemode}
	idx := 3
	where := " AND al.tenant_id = $1 AND al.livemode = $2"

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

	useCursor = !filter.AfterCreatedAt.IsZero() && filter.AfterID != ""
	if useCursor {
		where += fmt.Sprintf(" AND (al.created_at, al.id) < ($%d, $%d)", idx, idx+1)
		args = append(args, filter.AfterCreatedAt, filter.AfterID)
		idx += 2
	}

	return " WHERE " + where[5:], args, idx, useCursor // strip leading " AND "
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

	whereClause, args, idx, useCursor := buildListWhere(tenantID, postgres.Livemode(ctx), filter)

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

	query := auditListSelect + whereClause

	// Order by (created_at, id) DESC — id tiebreaker aligns with the
	// cursor predicate's tuple ordering so seek + ORDER BY stay in
	// lockstep regardless of microsecond-level ties.
	query += auditListOrder
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
		e, err := scanAuditEntry(rows)
		if err != nil {
			return nil, 0, err
		}
		entries = append(entries, e)
	}
	return entries, total, rows.Err()
}

// auditListSelect is the row shape shared by the paged read (Query) and the
// unbounded compliance export (Stream) — ONE select, so the CSV an operator
// hands an auditor cannot drift from what the dashboard showed them.
//
// LEFT JOIN api_keys so the UI can show a human-readable key name instead of the
// raw actor_id (e.g., "Production" vs "vlx_secret_live_abc123…"). Join is
// (tenant_id, id), which matches the api_keys PK's tenant scope.
//
// Also LEFT JOIN customers for actor_type='customer' rows (customer-portal-driven
// mutations) and users for actor_type='user' rows (dashboard session operators —
// actor identity from #225): the operator's email is the identity they recognize
// (the post-ADR-011 users table carries email only — no display_name column).
// users is a global (non-RLS) table so the join works under TxTenant. COALESCE
// order is safe — the joins are mutually exclusive in practice (an actor_id is
// exactly one of key / customer / user).
const auditListSelect = `SELECT al.id, al.tenant_id, al.actor_type, al.actor_id,
		COALESCE(NULLIF(k.name, ''), c.display_name, u.email::text, '') AS actor_name,
		al.action, al.resource_type, al.resource_id,
		COALESCE(al.resource_label,''), al.metadata,
		COALESCE(al.ip_address,''), COALESCE(al.request_id,''), al.created_at
		FROM audit_log al
		LEFT JOIN api_keys k ON k.id = al.actor_id AND k.tenant_id = al.tenant_id
		LEFT JOIN customers c ON c.id = al.actor_id AND c.tenant_id = al.tenant_id AND al.actor_type = 'customer'
		LEFT JOIN users u ON u.id = al.actor_id AND al.actor_type = 'user'`

// auditListOrder is newest-first, with the id tiebreaker the cursor predicate
// keys on.
const auditListOrder = " ORDER BY al.created_at DESC, al.id DESC"

func scanAuditEntry(rows *sql.Rows) (domain.AuditEntry, error) {
	var e domain.AuditEntry
	var metaJSON []byte
	if err := rows.Scan(&e.ID, &e.TenantID, &e.ActorType, &e.ActorID, &e.ActorName,
		&e.Action, &e.ResourceType, &e.ResourceID, &e.ResourceLabel,
		&metaJSON, &e.IPAddress, &e.RequestID, &e.CreatedAt); err != nil {
		return domain.AuditEntry{}, err
	}
	_ = json.Unmarshal(metaJSON, &e.Metadata)
	return e, nil
}

// Stream hands EVERY audit row matching filter to fn, newest first, in one
// snapshot transaction — no limit, no cap, no pagination.
//
// It backs the server-side audit-log CSV export (GET /v1/exports/audit-log.csv).
// The dashboard used to build that file by PAGING THE API IN THE BROWSER and
// stopping at 50,000 rows: a silent truncation of the compliance evidence
// ITSELF, handed to an auditor as if it were the whole record. There is no cap
// here on purpose — a truncated audit export is a lie about the log, and the
// only honest bound is "what the filter selected".
//
// filter.Limit / filter.Offset / the cursor fields are IGNORED: the export is
// defined by its predicates, not by a page.
//
// Rows are consumed as they arrive off the wire (database/sql + pgx stream
// them), so the handler writes CSV to the socket while Postgres is still
// producing — a million-row export does not first become a million-row slice in
// memory. An error from fn aborts the walk and surfaces to the caller, which is
// how a mid-stream failure reaches exportAbort's EXPORT_INCOMPLETE marker.
func (l *Logger) Stream(ctx context.Context, tenantID string, filter QueryFilter, fn func(domain.AuditEntry) error) error {
	tx, err := l.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return err
	}
	defer postgres.Rollback(tx)

	whereClause, args, _, _ := buildListWhere(tenantID, postgres.Livemode(ctx), filter)

	rows, err := tx.QueryContext(ctx, auditListSelect+whereClause+auditListOrder, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		e, err := scanAuditEntry(rows)
		if err != nil {
			return err
		}
		if err := fn(e); err != nil {
			return err
		}
	}
	return rows.Err()
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
	// more distinct actions than that; if they do, the cap drops the values
	// sorting LAST alphabetically (ORDER BY action), not the oldest ones.
	// Explicit tenant+livemode predicates for the same reason as Query: the
	// RLS policy's column-free bypass OR-arm blocks index quals, and these
	// two DISTINCTs run on every dashboard audit-page load.
	rows, err := tx.QueryContext(ctx,
		`SELECT DISTINCT action FROM audit_log
		 WHERE tenant_id = $1 AND livemode = $2 ORDER BY action LIMIT 500`,
		tenantID, postgres.Livemode(ctx))
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
		`SELECT DISTINCT resource_type FROM audit_log
		 WHERE tenant_id = $1 AND livemode = $2 ORDER BY resource_type LIMIT 500`,
		tenantID, postgres.Livemode(ctx))
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
