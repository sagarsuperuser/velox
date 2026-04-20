package testclock

import (
	"context"
	"database/sql"
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
	created_at, updated_at, deletes_after`

func (s *PostgresStore) Create(ctx context.Context, tenantID string, clk domain.TestClock) (domain.TestClock, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.TestClock{}, err
	}
	defer postgres.Rollback(tx)

	err = tx.QueryRowContext(ctx, `
		INSERT INTO test_clocks (tenant_id, name, frozen_time, status, deletes_after)
		VALUES ($1, $2, $3, 'ready', $4)
		RETURNING `+clockCols,
		tenantID, clk.Name, clk.FrozenTime, postgres.NullableTime(clk.DeletesAfter),
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

func (s *PostgresStore) Get(ctx context.Context, tenantID, id string) (domain.TestClock, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.TestClock{}, err
	}
	defer postgres.Rollback(tx)

	var clk domain.TestClock
	err = tx.QueryRowContext(ctx, `SELECT `+clockCols+` FROM test_clocks WHERE id = $1`, id).
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
		SELECT `+clockCols+` FROM test_clocks ORDER BY created_at DESC
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

func (s *PostgresStore) Delete(ctx context.Context, tenantID, id string) error {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return err
	}
	defer postgres.Rollback(tx)

	res, err := tx.ExecContext(ctx, `DELETE FROM test_clocks WHERE id = $1`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errs.ErrNotFound
	}
	return tx.Commit()
}

func (s *PostgresStore) MarkAdvancing(ctx context.Context, tenantID, id string, newFrozenTime time.Time) (domain.TestClock, error) {
	return s.transition(ctx, tenantID, id, "advancing", []string{"ready"},
		&newFrozenTime, "can only advance a clock in status=ready")
}

func (s *PostgresStore) CompleteAdvance(ctx context.Context, tenantID, id string) (domain.TestClock, error) {
	return s.transition(ctx, tenantID, id, "ready", []string{"advancing"},
		nil, "can only complete an advance from status=advancing")
}

func (s *PostgresStore) MarkFailed(ctx context.Context, tenantID, id string) (domain.TestClock, error) {
	return s.transition(ctx, tenantID, id, "internal_failure", []string{"advancing"},
		nil, "can only mark failed from status=advancing")
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
			WHERE id = $3 AND status = ANY($4) RETURNING ` + clockCols
		args = []any{to, *frozenTime, id, postgres.StringArray(allowedFrom)}
	} else {
		query = `UPDATE test_clocks SET status = $1, updated_at = now()
			WHERE id = $2 AND status = ANY($3) RETURNING ` + clockCols
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

	rows, err := tx.QueryContext(ctx, `
		SELECT id, tenant_id, code, customer_id, plan_id, status,
			trial_end_at, current_billing_period_start, current_billing_period_end,
			next_billing_at,
			COALESCE(test_clock_id,''), created_at, updated_at
		FROM subscriptions
		WHERE test_clock_id = $1
		ORDER BY created_at ASC
	`, clockID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var subs []domain.Subscription
	for rows.Next() {
		var sub domain.Subscription
		if err := rows.Scan(&sub.ID, &sub.TenantID, &sub.Code, &sub.CustomerID, &sub.PlanID,
			&sub.Status, &sub.TrialEndAt, &sub.CurrentBillingPeriodStart,
			&sub.CurrentBillingPeriodEnd, &sub.NextBillingAt,
			&sub.TestClockID, &sub.CreatedAt, &sub.UpdatedAt); err != nil {
			return nil, err
		}
		subs = append(subs, sub)
	}
	return subs, rows.Err()
}

func scanDest(c *domain.TestClock) []any {
	return []any{
		&c.ID, &c.TenantID, &c.Name, &c.FrozenTime, &c.Status,
		&c.CreatedAt, &c.UpdatedAt, &c.DeletesAfter,
	}
}
