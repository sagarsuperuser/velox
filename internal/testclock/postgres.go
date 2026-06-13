package testclock

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

type PostgresStore struct {
	db *postgres.DB
}

func NewPostgresStore(db *postgres.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

const clockCols = `id, tenant_id, name, frozen_time, status,
	created_at, updated_at, deleted_at,
	COALESCE(last_failure_reason,'')`

func (s *PostgresStore) Create(ctx context.Context, tenantID string, clk domain.TestClock) (domain.TestClock, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.TestClock{}, err
	}
	defer postgres.Rollback(tx)

	err = tx.QueryRowContext(ctx, `
		INSERT INTO test_clocks (tenant_id, name, frozen_time, status)
		VALUES ($1, $2, $3, 'ready')
		RETURNING `+clockCols,
		tenantID, clk.Name, clk.FrozenTime,
	).Scan(scanDest(&clk)...)
	if err != nil {
		// 23514 = check_violation; the livemode CHECK rejects any attempt to
		// insert a test clock from a live-mode session. Surface as 400 invalid
		// instead of leaking a raw SQL error string.
		if postgres.IsCheckViolation(err) {
			return domain.TestClock{}, errs.Invalid("livemode", "test clocks can only be created in test mode")
		}
		return domain.TestClock{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.TestClock{}, err
	}
	return clk, nil
}

// Exists is a narrow existence check used by callers that only need
// "is this clock attached-able?" (e.g., the customer service at
// customer-create time). Uses Get under the hood; ErrNotFound → false,
// other errors propagate. Soft-deleted clocks count as not-existing.
func (s *PostgresStore) Exists(ctx context.Context, tenantID, id string) (bool, error) {
	if _, err := s.Get(ctx, tenantID, id); err != nil {
		if errors.Is(err, errs.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (s *PostgresStore) Get(ctx context.Context, tenantID, id string) (domain.TestClock, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.TestClock{}, err
	}
	defer postgres.Rollback(tx)

	var clk domain.TestClock
	err = tx.QueryRowContext(ctx, `SELECT `+clockCols+` FROM test_clocks WHERE id = $1 AND deleted_at IS NULL`, id).
		Scan(scanDest(&clk)...)
	if err == sql.ErrNoRows {
		return domain.TestClock{}, errs.ErrNotFound
	}
	return clk, err
}

func (s *PostgresStore) List(ctx context.Context, tenantID string) ([]domain.TestClock, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	rows, err := tx.QueryContext(ctx, `
		SELECT `+clockCols+` FROM test_clocks
		WHERE deleted_at IS NULL
		ORDER BY created_at DESC LIMIT 500
	`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var clocks []domain.TestClock
	for rows.Next() {
		var clk domain.TestClock
		if err := rows.Scan(scanDest(&clk)...); err != nil {
			return nil, err
		}
		clocks = append(clocks, clk)
	}
	return clocks, rows.Err()
}

// Delete soft-deletes a clock and cascade-cancels every subscription
// pinned to it, atomically (ADR-016). Hard delete left silent
// orphans: subs detached via ON DELETE SET NULL with simulated
// next_billing_at the wall-clock scheduler couldn't reconcile.
//
// Idempotent on the clock: re-deleting an already-deleted clock
// returns errs.ErrNotFound (the live filter hides it). Idempotent
// on subs: the WHERE clause skips subs already canceled / archived
// so a partial-failure retry doesn't trample manual operator state.
//
// Generated invoices are intentionally NOT touched. Velox's
// invoice immutability rule (terminal-state finalized/paid/voided
// rows never mutate) takes precedence; the simulated timestamps on
// those invoices remain self-evident from the now-deleted clock,
// and any future audit query can still resolve them via id.
func (s *PostgresStore) Delete(ctx context.Context, tenantID, id string) error {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return err
	}
	defer postgres.Rollback(tx)

	res, err := tx.ExecContext(ctx,
		`UPDATE test_clocks SET deleted_at = now(), updated_at = now()
		 WHERE id = $1 AND deleted_at IS NULL`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errs.ErrNotFound
	}

	// Cascade-cancel pinned subs. Filter excludes subs already in
	// terminal states so an operator who manually canceled a sub
	// before deleting the clock keeps the more-specific state. Subs
	// KEEP their test_clock_id — they're now in a terminal state (no
	// further billing), and the retained pointer is the denormalized
	// "which clock did this belong to" cache (ADR-027). The stale
	// simulation-time period fields are exactly why subs are canceled
	// rather than detached: a detached sub would carry sim-time
	// next_billing_at the wall-clock scheduler can't reconcile and
	// would misfire (the bug ADR-016 was written to kill).
	if _, err := tx.ExecContext(ctx,
		`UPDATE subscriptions SET status = 'canceled', updated_at = now()
		 WHERE test_clock_id = $1 AND status NOT IN ('canceled', 'archived')`, id,
	); err != nil {
		return fmt.Errorf("cascade-cancel pinned subs: %w", err)
	}

	// Detach pinned customers — realize the customers.test_clock_id
	// `ON DELETE SET NULL` that ADR-016's soft-delete defeated (the row
	// isn't DELETEd, so the FK cascade never fires). Unlike subs, a
	// customer has no period fields to go stale, so detaching is safe:
	// it returns the customer to wall-clock so its NEXT subscription is
	// a clean, billable wall-clock sub instead of inheriting this dead
	// clock and stranding (excluded from both the cron — pinned — and
	// the catchup path — clock deleted). Without this the customer-level
	// pin (ADR-027) keeps spawning stranded subs after the clock is
	// gone. "Which customers were on this clock" stays answerable
	// through the canceled subs' retained pointers.
	if _, err := tx.ExecContext(ctx,
		`UPDATE customers SET test_clock_id = NULL, updated_at = now()
		 WHERE test_clock_id = $1`, id,
	); err != nil {
		return fmt.Errorf("detach pinned customers: %w", err)
	}

	return tx.Commit()
}

func (s *PostgresStore) MarkAdvancing(ctx context.Context, tenantID, id string, newFrozenTime time.Time) (domain.TestClock, error) {
	return s.transition(ctx, tenantID, id, "advancing", []string{"ready"},
		&newFrozenTime, "can only advance a clock in status=ready")
}

// CompleteAdvance flips advancing → ready and clears any prior
// last_failure_reason. The clear matters when the operator
// retried a failed advance and it succeeded — the dashboard
// shouldn't keep showing yesterday's error.
func (s *PostgresStore) CompleteAdvance(ctx context.Context, tenantID, id string) (domain.TestClock, error) {
	return s.transitionWithReason(ctx, tenantID, id, "ready",
		[]string{"advancing"}, "", true,
		"can only complete an advance from status=advancing")
}

// MarkFailed flips advancing → internal_failure and persists the
// reason. Caller truncates to ~500 chars; full payload stays in
// structured slog.
func (s *PostgresStore) MarkFailed(ctx context.Context, tenantID, id, reason string) (domain.TestClock, error) {
	return s.transitionWithReason(ctx, tenantID, id, "internal_failure",
		[]string{"advancing"}, reason, false,
		"can only mark failed from status=advancing")
}

// RetryFromFailed flips internal_failure → advancing, clearing
// the failure reason. Frozen_time stays at its current value —
// the catchup loop is idempotent on subs whose next_billing_at
// has already passed it. ADR-018.
func (s *PostgresStore) RetryFromFailed(ctx context.Context, tenantID, id string) (domain.TestClock, error) {
	return s.transitionWithReason(ctx, tenantID, id, "advancing",
		[]string{"internal_failure"}, "", true,
		"can only retry from status=internal_failure")
}

// transitionWithReason is the CAS helper for transitions that
// either write or clear last_failure_reason. clearReason=true
// means SET last_failure_reason = NULL; false means SET
// last_failure_reason = $reason. Used by CompleteAdvance,
// MarkFailed, RetryFromFailed.
func (s *PostgresStore) transitionWithReason(
	ctx context.Context, tenantID, id, to string, allowedFrom []string,
	reason string, clearReason bool, wrongStateMsg string,
) (domain.TestClock, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.TestClock{}, err
	}
	defer postgres.Rollback(tx)

	var clk domain.TestClock
	var query string
	var args []any
	if clearReason {
		query = `UPDATE test_clocks SET status = $1, last_failure_reason = NULL,
			updated_at = now()
			WHERE id = $2 AND status = ANY($3) AND deleted_at IS NULL
			RETURNING ` + clockCols
		args = []any{to, id, postgres.StringArray(allowedFrom)}
	} else {
		query = `UPDATE test_clocks SET status = $1, last_failure_reason = $2,
			updated_at = now()
			WHERE id = $3 AND status = ANY($4) AND deleted_at IS NULL
			RETURNING ` + clockCols
		args = []any{to, truncateReason(reason), id, postgres.StringArray(allowedFrom)}
	}
	err = tx.QueryRowContext(ctx, query, args...).Scan(scanDest(&clk)...)
	if err == sql.ErrNoRows {
		var current string
		err2 := tx.QueryRowContext(ctx, `SELECT status FROM test_clocks WHERE id = $1`, id).Scan(&current)
		if err2 == sql.ErrNoRows {
			return domain.TestClock{}, errs.ErrNotFound
		}
		if err2 != nil {
			return domain.TestClock{}, err2
		}
		return domain.TestClock{}, errs.InvalidState(fmt.Sprintf("%s, current status: %s", wrongStateMsg, current))
	}
	if err != nil {
		return domain.TestClock{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.TestClock{}, err
	}
	return clk, nil
}

// truncateReason caps the failure reason at a length the
// dashboard can render in a single inline panel without scroll.
// Full payload stays in slog for ops grep.
func truncateReason(s string) string {
	const max = 500
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

// transition is the atomic CAS helper for status changes: UPDATE … WHERE
// status = ANY(allowedFrom) returning the row. Returning zero rows is
// ambiguous (missing row vs wrong state), so a second lookup distinguishes.
func (s *PostgresStore) transition(ctx context.Context, tenantID, id, to string, allowedFrom []string, frozenTime *time.Time, wrongStateMsg string) (domain.TestClock, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.TestClock{}, err
	}
	defer postgres.Rollback(tx)

	var clk domain.TestClock
	var query string
	var args []any
	if frozenTime != nil {
		query = `UPDATE test_clocks SET status = $1, frozen_time = $2, updated_at = now()
			WHERE id = $3 AND status = ANY($4) AND deleted_at IS NULL RETURNING ` + clockCols
		args = []any{to, *frozenTime, id, postgres.StringArray(allowedFrom)}
	} else {
		query = `UPDATE test_clocks SET status = $1, updated_at = now()
			WHERE id = $2 AND status = ANY($3) AND deleted_at IS NULL RETURNING ` + clockCols
		args = []any{to, id, postgres.StringArray(allowedFrom)}
	}
	err = tx.QueryRowContext(ctx, query, args...).Scan(scanDest(&clk)...)
	if err == sql.ErrNoRows {
		var current string
		err2 := tx.QueryRowContext(ctx, `SELECT status FROM test_clocks WHERE id = $1`, id).Scan(&current)
		if err2 == sql.ErrNoRows {
			return domain.TestClock{}, errs.ErrNotFound
		}
		if err2 != nil {
			return domain.TestClock{}, err2
		}
		return domain.TestClock{}, errs.InvalidState(fmt.Sprintf("%s, current status: %s", wrongStateMsg, current))
	}
	if err != nil {
		return domain.TestClock{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.TestClock{}, err
	}
	return clk, nil
}

// ListSubscriptionsOnClock returns subscriptions attached to the given clock.
// This crosses into the subscription table on purpose — the test clock owns
// the catchup orchestration and needs the sub rows to drive it. We keep the
// query narrow (id + fields the catchup consults) to avoid duplicating the
// subscription package's full scan.
func (s *PostgresStore) ListSubscriptionsOnClock(ctx context.Context, tenantID, clockID string) ([]domain.Subscription, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	// Items aren't hydrated here — this list is used by the test clock
	// advance path which only reads scheduling fields (trial, billing period,
	// next_billing_at). If a caller needs item data they should go through
	// the subscription package's Get/List which hydrate Items explicitly.
	rows, err := tx.QueryContext(ctx, `
		SELECT id, tenant_id, code, customer_id, status,
			trial_end_at, current_billing_period_start, current_billing_period_end,
			next_billing_at,
			COALESCE(test_clock_id,''), created_at, updated_at
		FROM subscriptions
		WHERE test_clock_id = $1
		ORDER BY created_at ASC LIMIT 1000
	`, clockID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var subs []domain.Subscription
	for rows.Next() {
		var sub domain.Subscription
		if err := rows.Scan(&sub.ID, &sub.TenantID, &sub.Code, &sub.CustomerID,
			&sub.Status, &sub.TrialEndAt, &sub.CurrentBillingPeriodStart,
			&sub.CurrentBillingPeriodEnd, &sub.NextBillingAt,
			&sub.TestClockID, &sub.CreatedAt, &sub.UpdatedAt); err != nil {
			return nil, err
		}
		subs = append(subs, sub)
	}
	return subs, rows.Err()
}

// ListAllAdvancing scans test_clocks for rows in status='advancing'
// across every tenant. Used at boot to recover catchup jobs that
// were in-flight when the previous process exited. RLS-bypassed
// (TxBypass) because the recovery path runs before any tenant ctx
// is established and needs to see all tenants. Limited to 1000 to
// bound the recovery enqueue burst — at expected pre-launch
// volumes there will rarely be more than 0-1 stuck clocks; the
// limit is a sanity cap, not a paging target.
func (s *PostgresStore) ListAllAdvancing(ctx context.Context) ([]domain.TestClock, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	rows, err := tx.QueryContext(ctx, `
		SELECT `+clockCols+` FROM test_clocks
		WHERE status = 'advancing' AND deleted_at IS NULL
		ORDER BY updated_at ASC
		LIMIT 1000
	`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var clocks []domain.TestClock
	for rows.Next() {
		var clk domain.TestClock
		if err := rows.Scan(scanDest(&clk)...); err != nil {
			return nil, err
		}
		clocks = append(clocks, clk)
	}
	return clocks, rows.Err()
}

func scanDest(c *domain.TestClock) []any {
	return []any{
		&c.ID, &c.TenantID, &c.Name, &c.FrozenTime, &c.Status,
		&c.CreatedAt, &c.UpdatedAt, &c.DeletedAt,
		&c.LastFailureReason,
	}
}
