package planmigration

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

// Store is the persistence surface for plan migration history. The
// per-customer plan_id swaps live on subscription_items (handled by the
// subscription store); this store owns the cohort-level audit row that
// answers "what migration ran across these N customers, when, by whom,
// with what est. delta?"
type Store interface {
	// Insert records a new plan migration row. Returns the persisted
	// row (with id + created_at filled in by Postgres). Uniqueness on
	// (tenant_id, idempotency_key) means callers must check for
	// errs.ErrAlreadyExists and reuse the prior row instead.
	Insert(ctx context.Context, tenantID string, row Migration) (Migration, error)

	// GetByIdempotencyKey returns the prior migration for (tenant, key)
	// or errs.ErrNotFound if none exists. Used by the commit handler to
	// short-circuit a replay.
	GetByIdempotencyKey(ctx context.Context, tenantID, idempotencyKey string) (Migration, error)

	// SetAuditLogID stamps the audit_log_id on a migration row after the
	// audit entry is persisted. Separate call so the migration row is
	// available to the audit metadata write (the audit row references
	// the migration id), and we can backfill the linkage.
	SetAuditLogID(ctx context.Context, tenantID, migrationID, auditLogID string) error

	// UpdateAppliedCount stamps the final applied_count after the
	// per-item swaps complete. Separate from Insert because the count
	// isn't known until the cohort is walked.
	UpdateAppliedCount(ctx context.Context, tenantID, migrationID string, count int) error

	// List returns recent migrations for a tenant in reverse chrono order.
	// limit caps page size; cursor is the created_at + id of the last row
	// from the previous page (empty for first page). Returns the rows and
	// the next-page cursor (empty when no more pages).
	List(ctx context.Context, tenantID string, limit int, cursor string) ([]Migration, string, error)
}

// Migration is the in-memory shape of a row in plan_migrations. JSON tags
// are intentionally absent — handler.go owns the wire shape and converts.
type Migration struct {
	ID             string
	TenantID       string
	IdempotencyKey string
	FromPlanID     string
	ToPlanID       string
	CustomerFilter CustomerFilter
	Effective      string
	AppliedCount   int
	Totals         []MigrationTotal
	AppliedBy      string
	AppliedByType  string
	AuditLogID     string
	CreatedAt      time.Time
}

// CustomerFilter selects the cohort for a migration. Three shapes:
//
//	Type=="all"  → every active subscription on FromPlanID for the tenant.
//	Type=="ids"  → only the subscriptions whose customer_id ∈ IDs.
//	Type=="tag"  → reserved for tag-based filters (no implementation yet
//	               because customers don't have a tag column; the wire
//	               shape accepts it so frontend mocks compile, and the
//	               service layer rejects with a coded error pending the
//	               customer-tag schema).
type CustomerFilter struct {
	Type  string   `json:"type"`
	IDs   []string `json:"ids,omitempty"`
	Value string   `json:"value,omitempty"`
}

// MigrationTotal is a cohort roll-up keyed by currency. Snapshotted at
// commit time so list views don't have to re-run the preview engine.
type MigrationTotal struct {
	Currency          string `json:"currency"`
	BeforeAmountCents int64  `json:"before_amount_cents"`
	AfterAmountCents  int64  `json:"after_amount_cents"`
	DeltaAmountCents  int64  `json:"delta_amount_cents"`
}

// PostgresStore implements Store against the plan_migrations table.
type PostgresStore struct {
	db *postgres.DB
}

// NewPostgresStore wires the store against the supplied DB. RLS is
// applied via TxTenant on every read/write — cross-tenant queries fall
// back to "no rows" so the store layer is implicitly tenant-isolated.
func NewPostgresStore(db *postgres.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

const migrationCols = `id, tenant_id, idempotency_key, from_plan_id, to_plan_id,
	customer_filter, effective, applied_count, totals, applied_by,
	applied_by_type, COALESCE(audit_log_id,''), created_at`

func (s *PostgresStore) Insert(ctx context.Context, tenantID string, row Migration) (Migration, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return Migration{}, err
	}
	defer postgres.Rollback(tx)

	id := postgres.NewID("vlx_pmig")
	if row.ID != "" {
		id = row.ID
	}
	filterJSON, err := json.Marshal(row.CustomerFilter)
	if err != nil {
		return Migration{}, fmt.Errorf("marshal customer_filter: %w", err)
	}
	totalsJSON, err := json.Marshal(row.Totals)
	if err != nil {
		return Migration{}, fmt.Errorf("marshal totals: %w", err)
	}
	if row.Totals == nil {
		totalsJSON = []byte("[]")
	}

	var stored Migration
	err = tx.QueryRowContext(ctx, `
		INSERT INTO plan_migrations
		    (id, tenant_id, idempotency_key, from_plan_id, to_plan_id,
		     customer_filter, effective, applied_count, totals,
		     applied_by, applied_by_type)
		VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7,$8,$9::jsonb,$10,$11)
		RETURNING `+migrationCols,
		id, tenantID, row.IdempotencyKey, row.FromPlanID, row.ToPlanID,
		string(filterJSON), row.Effective, row.AppliedCount, string(totalsJSON),
		row.AppliedBy, row.AppliedByType,
	).Scan(scanMigrationDest(&stored)...)
	if err != nil {
		if postgres.UniqueViolationConstraint(err) != "" {
			return Migration{}, errs.ErrAlreadyExists
		}
		return Migration{}, err
	}
	if err := tx.Commit(); err != nil {
		return Migration{}, err
	}
	return stored, nil
}

func (s *PostgresStore) GetByIdempotencyKey(ctx context.Context, tenantID, idempotencyKey string) (Migration, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return Migration{}, err
	}
	defer postgres.Rollback(tx)

	var row Migration
	err = tx.QueryRowContext(ctx, `
		SELECT `+migrationCols+`
		FROM plan_migrations
		WHERE idempotency_key = $1
		LIMIT 1`, idempotencyKey,
	).Scan(scanMigrationDest(&row)...)
	if err == sql.ErrNoRows {
		return Migration{}, errs.ErrNotFound
	}
	if err != nil {
		return Migration{}, err
	}
	return row, nil
}

func (s *PostgresStore) SetAuditLogID(ctx context.Context, tenantID, migrationID, auditLogID string) error {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return err
	}
	defer postgres.Rollback(tx)

	res, err := tx.ExecContext(ctx, `
		UPDATE plan_migrations SET audit_log_id = $1
		WHERE id = $2`, auditLogID, migrationID)
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return errs.ErrNotFound
	}
	return tx.Commit()
}

func (s *PostgresStore) UpdateAppliedCount(ctx context.Context, tenantID, migrationID string, count int) error {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return err
	}
	defer postgres.Rollback(tx)

	if _, err := tx.ExecContext(ctx, `
		UPDATE plan_migrations SET applied_count = $1 WHERE id = $2`,
		count, migrationID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *PostgresStore) List(ctx context.Context, tenantID string, limit int, cursor string) ([]Migration, string, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return nil, "", err
	}
	defer postgres.Rollback(tx)

	if limit <= 0 || limit > 100 {
		limit = 25
	}

	// Cursor pagination. The cursor is the last row's id; we fetch
	// limit+1 to know whether a next page exists. ORDER BY created_at
	// DESC, id DESC is a stable total order even when two rows share
	// created_at (gen_random_bytes prefix breaks ties).
	args := []any{limit + 1}
	where := ""
	if cursor != "" {
		where = ` WHERE (created_at, id) < (
			SELECT created_at, id FROM plan_migrations WHERE id = $2 LIMIT 1)`
		args = append(args, cursor)
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT `+migrationCols+`
		FROM plan_migrations`+where+`
		ORDER BY created_at DESC, id DESC
		LIMIT $1`, args...)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = rows.Close() }()

	out := make([]Migration, 0, limit)
	for rows.Next() {
		var m Migration
		if err := rows.Scan(scanMigrationDest(&m)...); err != nil {
			return nil, "", err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	nextCursor := ""
	if len(out) > limit {
		nextCursor = out[limit-1].ID
		out = out[:limit]
	}
	return out, nextCursor, nil
}

// scanMigrationDest returns the destination pointers to scan one
// plan_migrations row into the Migration fields. Pulled out so the
// Insert / Get / List paths share the column ordering exactly.
func scanMigrationDest(m *Migration) []any {
	return []any{
		&m.ID, &m.TenantID, &m.IdempotencyKey, &m.FromPlanID, &m.ToPlanID,
		&migrationFilterScanner{m: m},
		&m.Effective, &m.AppliedCount,
		&migrationTotalsScanner{m: m},
		&m.AppliedBy, &m.AppliedByType, &m.AuditLogID, &m.CreatedAt,
	}
}

// migrationFilterScanner unmarshals the customer_filter JSONB column
// into the Migration's CustomerFilter struct. sql.Scanner over the
// receiver lets us keep scanMigrationDest's signature uniform with the
// other simple columns.
type migrationFilterScanner struct{ m *Migration }

func (s *migrationFilterScanner) Scan(src any) error {
	if src == nil {
		s.m.CustomerFilter = CustomerFilter{}
		return nil
	}
	var b []byte
	switch v := src.(type) {
	case []byte:
		b = v
	case string:
		b = []byte(v)
	default:
		return fmt.Errorf("unsupported customer_filter type %T", src)
	}
	if len(b) == 0 {
		s.m.CustomerFilter = CustomerFilter{}
		return nil
	}
	return json.Unmarshal(b, &s.m.CustomerFilter)
}

// migrationTotalsScanner unmarshals the totals JSONB column. Always-array
// shape: empty input becomes []MigrationTotal{} so callers don't need
// nil guards.
type migrationTotalsScanner struct{ m *Migration }

func (s *migrationTotalsScanner) Scan(src any) error {
	if src == nil {
		s.m.Totals = []MigrationTotal{}
		return nil
	}
	var b []byte
	switch v := src.(type) {
	case []byte:
		b = v
	case string:
		b = []byte(v)
	default:
		return fmt.Errorf("unsupported totals type %T", src)
	}
	if len(b) == 0 {
		s.m.Totals = []MigrationTotal{}
		return nil
	}
	if err := json.Unmarshal(b, &s.m.Totals); err != nil {
		return err
	}
	if s.m.Totals == nil {
		s.m.Totals = []MigrationTotal{}
	}
	return nil
}
