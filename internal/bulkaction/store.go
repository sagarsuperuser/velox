// Package bulkaction implements operator-initiated cohort operations
// across many customers — apply coupon, schedule cancel — with the same
// preview/commit + idempotency-key pattern plan migrations use (see
// internal/planmigration). One bulk_actions row per run; per-customer
// outcomes live in audit_log keyed by bulk_action_id metadata.
package bulkaction

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

// Store is the persistence surface for bulk actions. Each method opens a
// tenant-scoped tx via postgres.TxTenant; cross-tenant IDs naturally 404
// because RLS hides the row.
type Store interface {
	// Insert records a new bulk_actions row. Returns the persisted row
	// (with id + created_at filled in by Postgres). Uniqueness on
	// (tenant_id, idempotency_key) means callers must check for
	// errs.ErrAlreadyExists and reuse the prior row instead.
	Insert(ctx context.Context, tenantID string, row Action) (Action, error)

	// GetByIdempotencyKey returns the prior bulk action for (tenant, key)
	// or errs.ErrNotFound if none exists. Used by the commit handlers to
	// short-circuit a replay.
	GetByIdempotencyKey(ctx context.Context, tenantID, idempotencyKey string) (Action, error)

	// Get returns a single bulk_actions row by id, or errs.ErrNotFound.
	Get(ctx context.Context, tenantID, id string) (Action, error)

	// UpdateProgress stamps the cohort counters + status + completed_at +
	// errors snapshot after the per-target loop completes.
	UpdateProgress(ctx context.Context, tenantID, id string, status string, target, succeeded, failed int, errs []TargetError, completedAt *time.Time) error

	// List returns recent bulk actions for a tenant in reverse chrono
	// order, optionally filtered by status / action_type. Cursor pagination
	// using the last row's id (created_at + id total order).
	List(ctx context.Context, tenantID string, filter ListFilter) ([]Action, string, error)
}

// ListFilter is the query shape for the list endpoint.
type ListFilter struct {
	Status     string
	ActionType string
	Limit      int
	Cursor     string
}

// Action is the in-memory shape of a row in bulk_actions. JSON tags are
// intentionally absent — handler.go owns the wire shape and converts.
type Action struct {
	ID             string
	TenantID       string
	IdempotencyKey string
	ActionType     string
	CustomerFilter CustomerFilter
	Params         map[string]any
	Status         string
	TargetCount    int
	SucceededCount int
	FailedCount    int
	Errors         []TargetError
	CreatedBy      string
	CreatedAt      time.Time
	CompletedAt    *time.Time
}

// CustomerFilter selects the cohort. Same three-mode shape as
// planmigration.CustomerFilter so the wire shape feels consistent. v1
// rejects "tag" at the service layer.
type CustomerFilter struct {
	Type  string   `json:"type"`
	IDs   []string `json:"ids,omitempty"`
	Value string   `json:"value,omitempty"`
}

// TargetError captures one customer's failure during commit. Surfaced in
// the errors[] JSONB column and returned in the list/detail response so
// operators see exactly which customers were skipped and why.
type TargetError struct {
	CustomerID string `json:"customer_id"`
	Error      string `json:"error"`
}

// Action types — keep in sync with the CHECK constraint in
// migration 0061_bulk_actions.up.sql.
const (
	ActionApplyCoupon    = "apply_coupon"
	ActionScheduleCancel = "schedule_cancel"
)

// Status values — keep in sync with the CHECK constraint.
const (
	StatusPending   = "pending"
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusPartial   = "partial"
	StatusFailed    = "failed"
)

// PostgresStore implements Store against the bulk_actions table.
type PostgresStore struct {
	db *postgres.DB
}

func NewPostgresStore(db *postgres.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

const actionCols = `id, tenant_id, idempotency_key, action_type,
	customer_filter, params, status, target_count, succeeded_count,
	failed_count, COALESCE(errors,'[]'::jsonb), COALESCE(created_by,''),
	created_at, completed_at`

func (s *PostgresStore) Insert(ctx context.Context, tenantID string, row Action) (Action, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return Action{}, err
	}
	defer postgres.Rollback(tx)

	id := postgres.NewID("vlx_bact")
	if row.ID != "" {
		id = row.ID
	}
	filterJSON, err := json.Marshal(row.CustomerFilter)
	if err != nil {
		return Action{}, fmt.Errorf("marshal customer_filter: %w", err)
	}
	paramsJSON, err := json.Marshal(row.Params)
	if err != nil {
		return Action{}, fmt.Errorf("marshal params: %w", err)
	}
	if row.Params == nil {
		paramsJSON = []byte("{}")
	}
	errsJSON, err := json.Marshal(row.Errors)
	if err != nil {
		return Action{}, fmt.Errorf("marshal errors: %w", err)
	}
	if row.Errors == nil {
		errsJSON = []byte("[]")
	}
	status := row.Status
	if status == "" {
		status = StatusPending
	}

	var stored Action
	err = tx.QueryRowContext(ctx, `
		INSERT INTO bulk_actions
		    (id, tenant_id, idempotency_key, action_type, customer_filter,
		     params, status, target_count, succeeded_count, failed_count,
		     errors, created_by)
		VALUES ($1,$2,$3,$4,$5::jsonb,$6::jsonb,$7,$8,$9,$10,$11::jsonb,$12)
		RETURNING `+actionCols,
		id, tenantID, row.IdempotencyKey, row.ActionType, string(filterJSON),
		string(paramsJSON), status, row.TargetCount, row.SucceededCount,
		row.FailedCount, string(errsJSON), nullIfEmpty(row.CreatedBy),
	).Scan(scanActionDest(&stored)...)
	if err != nil {
		if postgres.UniqueViolationConstraint(err) != "" {
			return Action{}, errs.ErrAlreadyExists
		}
		return Action{}, err
	}
	if err := tx.Commit(); err != nil {
		return Action{}, err
	}
	return stored, nil
}

func (s *PostgresStore) GetByIdempotencyKey(ctx context.Context, tenantID, idempotencyKey string) (Action, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return Action{}, err
	}
	defer postgres.Rollback(tx)

	var row Action
	err = tx.QueryRowContext(ctx, `
		SELECT `+actionCols+`
		FROM bulk_actions
		WHERE idempotency_key = $1
		LIMIT 1`, idempotencyKey,
	).Scan(scanActionDest(&row)...)
	if err == sql.ErrNoRows {
		return Action{}, errs.ErrNotFound
	}
	if err != nil {
		return Action{}, err
	}
	return row, nil
}

func (s *PostgresStore) Get(ctx context.Context, tenantID, id string) (Action, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return Action{}, err
	}
	defer postgres.Rollback(tx)

	var row Action
	err = tx.QueryRowContext(ctx, `
		SELECT `+actionCols+`
		FROM bulk_actions
		WHERE id = $1
		LIMIT 1`, id,
	).Scan(scanActionDest(&row)...)
	if err == sql.ErrNoRows {
		return Action{}, errs.ErrNotFound
	}
	if err != nil {
		return Action{}, err
	}
	return row, nil
}

func (s *PostgresStore) UpdateProgress(ctx context.Context, tenantID, id string, status string, target, succeeded, failed int, errors []TargetError, completedAt *time.Time) error {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return err
	}
	defer postgres.Rollback(tx)

	errsJSON, err := json.Marshal(errors)
	if err != nil {
		return fmt.Errorf("marshal errors: %w", err)
	}
	if errors == nil {
		errsJSON = []byte("[]")
	}

	res, err := tx.ExecContext(ctx, `
		UPDATE bulk_actions
		SET status = $1,
		    target_count = $2,
		    succeeded_count = $3,
		    failed_count = $4,
		    errors = $5::jsonb,
		    completed_at = $6
		WHERE id = $7`,
		status, target, succeeded, failed, string(errsJSON), completedAt, id,
	)
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

func (s *PostgresStore) List(ctx context.Context, tenantID string, filter ListFilter) ([]Action, string, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return nil, "", err
	}
	defer postgres.Rollback(tx)

	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 25
	}

	args := []any{limit + 1}
	idx := 2
	where := ""
	if filter.Status != "" {
		where += fmt.Sprintf(" AND status = $%d", idx)
		args = append(args, filter.Status)
		idx++
	}
	if filter.ActionType != "" {
		where += fmt.Sprintf(" AND action_type = $%d", idx)
		args = append(args, filter.ActionType)
		idx++
	}
	if filter.Cursor != "" {
		where += fmt.Sprintf(` AND (created_at, id) < (
			SELECT created_at, id FROM bulk_actions WHERE id = $%d LIMIT 1)`, idx)
		args = append(args, filter.Cursor)
		// idx not incremented further; nothing follows in this filter chain.
	}
	whereClause := ""
	if where != "" {
		whereClause = " WHERE " + where[5:] // strip leading " AND "
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT `+actionCols+`
		FROM bulk_actions`+whereClause+`
		ORDER BY created_at DESC, id DESC
		LIMIT $1`, args...)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = rows.Close() }()

	out := make([]Action, 0, limit)
	for rows.Next() {
		var a Action
		if err := rows.Scan(scanActionDest(&a)...); err != nil {
			return nil, "", err
		}
		out = append(out, a)
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

// scanActionDest returns the destination pointers to scan one bulk_actions
// row into the Action fields. Pulled out so Insert / Get / List share the
// column ordering exactly.
func scanActionDest(a *Action) []any {
	return []any{
		&a.ID, &a.TenantID, &a.IdempotencyKey, &a.ActionType,
		&actionFilterScanner{a: a},
		&actionParamsScanner{a: a},
		&a.Status, &a.TargetCount, &a.SucceededCount, &a.FailedCount,
		&actionErrorsScanner{a: a},
		&a.CreatedBy, &a.CreatedAt, &a.CompletedAt,
	}
}

type actionFilterScanner struct{ a *Action }

func (s *actionFilterScanner) Scan(src any) error {
	if src == nil {
		s.a.CustomerFilter = CustomerFilter{}
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
		s.a.CustomerFilter = CustomerFilter{}
		return nil
	}
	return json.Unmarshal(b, &s.a.CustomerFilter)
}

type actionParamsScanner struct{ a *Action }

func (s *actionParamsScanner) Scan(src any) error {
	if src == nil {
		s.a.Params = map[string]any{}
		return nil
	}
	var b []byte
	switch v := src.(type) {
	case []byte:
		b = v
	case string:
		b = []byte(v)
	default:
		return fmt.Errorf("unsupported params type %T", src)
	}
	if len(b) == 0 {
		s.a.Params = map[string]any{}
		return nil
	}
	if err := json.Unmarshal(b, &s.a.Params); err != nil {
		return err
	}
	if s.a.Params == nil {
		s.a.Params = map[string]any{}
	}
	return nil
}

type actionErrorsScanner struct{ a *Action }

func (s *actionErrorsScanner) Scan(src any) error {
	if src == nil {
		s.a.Errors = []TargetError{}
		return nil
	}
	var b []byte
	switch v := src.(type) {
	case []byte:
		b = v
	case string:
		b = []byte(v)
	default:
		return fmt.Errorf("unsupported errors type %T", src)
	}
	if len(b) == 0 {
		s.a.Errors = []TargetError{}
		return nil
	}
	if err := json.Unmarshal(b, &s.a.Errors); err != nil {
		return err
	}
	if s.a.Errors == nil {
		s.a.Errors = []TargetError{}
	}
	return nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
