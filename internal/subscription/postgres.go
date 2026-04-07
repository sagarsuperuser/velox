package subscription

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

const subCols = `id, tenant_id, code, display_name, customer_id, plan_id, status, billing_time,
	trial_start_at, trial_end_at, started_at, activated_at, canceled_at,
	COALESCE(previous_plan_id,''), plan_changed_at,
	current_billing_period_start, current_billing_period_end, next_billing_at,
	created_at, updated_at`

func (s *PostgresStore) Create(ctx context.Context, tenantID string, sub domain.Subscription) (domain.Subscription, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Subscription{}, err
	}
	defer postgres.Rollback(tx)

	id := postgres.NewID("vlx_sub")
	now := time.Now().UTC()

	err = tx.QueryRowContext(ctx, `
		INSERT INTO subscriptions (id, tenant_id, code, display_name, customer_id, plan_id, status,
			billing_time, trial_start_at, trial_end_at, started_at,
			current_billing_period_start, current_billing_period_end, next_billing_at,
			created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$15)
		RETURNING `+subCols,
		id, tenantID, sub.Code, sub.DisplayName, sub.CustomerID, sub.PlanID,
		sub.Status, sub.BillingTime, postgres.NullableTime(sub.TrialStartAt),
		postgres.NullableTime(sub.TrialEndAt), postgres.NullableTime(sub.StartedAt),
		postgres.NullableTime(sub.CurrentBillingPeriodStart),
		postgres.NullableTime(sub.CurrentBillingPeriodEnd),
		postgres.NullableTime(sub.NextBillingAt), now,
	).Scan(scanSubDest(&sub)...)

	if err != nil {
		if postgres.IsUniqueViolation(err) {
			return domain.Subscription{}, fmt.Errorf("%w: subscription code %q", errs.ErrAlreadyExists, sub.Code)
		}
		return domain.Subscription{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Subscription{}, err
	}
	return sub, nil
}

func (s *PostgresStore) Get(ctx context.Context, tenantID, id string) (domain.Subscription, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Subscription{}, err
	}
	defer postgres.Rollback(tx)

	var sub domain.Subscription
	err = tx.QueryRowContext(ctx, `SELECT `+subCols+` FROM subscriptions WHERE id = $1`, id).
		Scan(scanSubDest(&sub)...)
	if err == sql.ErrNoRows {
		return domain.Subscription{}, errs.ErrNotFound
	}
	return sub, err
}

func (s *PostgresStore) List(ctx context.Context, filter ListFilter) ([]domain.Subscription, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, filter.TenantID)
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	where, args := buildSubWhere(filter)
	query := `SELECT ` + subCols + ` FROM subscriptions` + where + ` ORDER BY created_at DESC LIMIT $` + fmt.Sprintf("%d", len(args)+1) + ` OFFSET $` + fmt.Sprintf("%d", len(args)+2)
	args = append(args, limit, filter.Offset)

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subs []domain.Subscription
	for rows.Next() {
		var sub domain.Subscription
		if err := rows.Scan(scanSubDest(&sub)...); err != nil {
			return nil, err
		}
		subs = append(subs, sub)
	}
	return subs, rows.Err()
}

func (s *PostgresStore) Update(ctx context.Context, tenantID string, sub domain.Subscription) (domain.Subscription, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Subscription{}, err
	}
	defer postgres.Rollback(tx)

	now := time.Now().UTC()
	err = tx.QueryRowContext(ctx, `
		UPDATE subscriptions SET status = $1, activated_at = $2, canceled_at = $3,
			trial_start_at = $4, trial_end_at = $5,
			plan_id = $6, previous_plan_id = $7, plan_changed_at = $8,
			updated_at = $9
		WHERE id = $10
		RETURNING `+subCols,
		sub.Status, postgres.NullableTime(sub.ActivatedAt), postgres.NullableTime(sub.CanceledAt),
		postgres.NullableTime(sub.TrialStartAt), postgres.NullableTime(sub.TrialEndAt),
		sub.PlanID, postgres.NullableString(sub.PreviousPlanID), postgres.NullableTime(sub.PlanChangedAt),
		now, sub.ID,
	).Scan(scanSubDest(&sub)...)

	if err == sql.ErrNoRows {
		return domain.Subscription{}, errs.ErrNotFound
	}
	if err != nil {
		return domain.Subscription{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Subscription{}, err
	}
	return sub, nil
}

func (s *PostgresStore) GetDueBilling(ctx context.Context, before time.Time, limit int) ([]domain.Subscription, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	if limit <= 0 {
		limit = 50
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT `+subCols+` FROM subscriptions
		WHERE status = 'active' AND next_billing_at <= $1
		ORDER BY next_billing_at ASC LIMIT $2
	`, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subs []domain.Subscription
	for rows.Next() {
		var sub domain.Subscription
		if err := rows.Scan(scanSubDest(&sub)...); err != nil {
			return nil, err
		}
		subs = append(subs, sub)
	}
	return subs, rows.Err()
}

func (s *PostgresStore) UpdateBillingCycle(ctx context.Context, tenantID, id string, periodStart, periodEnd, nextBillingAt time.Time) error {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return err
	}
	defer postgres.Rollback(tx)

	result, err := tx.ExecContext(ctx, `
		UPDATE subscriptions SET current_billing_period_start = $1, current_billing_period_end = $2,
			next_billing_at = $3, updated_at = $4
		WHERE id = $5
	`, periodStart, periodEnd, nextBillingAt, time.Now().UTC(), id)
	if err != nil {
		return err
	}

	n, _ := result.RowsAffected()
	if n == 0 {
		return errs.ErrNotFound
	}
	return tx.Commit()
}

func scanSubDest(s *domain.Subscription) []any {
	return []any{
		&s.ID, &s.TenantID, &s.Code, &s.DisplayName, &s.CustomerID, &s.PlanID,
		&s.Status, &s.BillingTime, &s.TrialStartAt, &s.TrialEndAt, &s.StartedAt,
		&s.ActivatedAt, &s.CanceledAt, &s.PreviousPlanID, &s.PlanChangedAt,
		&s.CurrentBillingPeriodStart,
		&s.CurrentBillingPeriodEnd, &s.NextBillingAt, &s.CreatedAt, &s.UpdatedAt,
	}
}

func buildSubWhere(f ListFilter) (string, []any) {
	var clauses []string
	var args []any
	idx := 1

	if f.CustomerID != "" {
		clauses = append(clauses, fmt.Sprintf("customer_id = $%d", idx))
		args = append(args, f.CustomerID)
		idx++
	}
	if f.PlanID != "" {
		clauses = append(clauses, fmt.Sprintf("plan_id = $%d", idx))
		args = append(args, f.PlanID)
		idx++
	}
	if f.Status != "" {
		clauses = append(clauses, fmt.Sprintf("status = $%d", idx))
		args = append(args, f.Status)
		idx++
	}

	if len(clauses) == 0 {
		return "", args
	}
	where := " WHERE "
	for i, c := range clauses {
		if i > 0 {
			where += " AND "
		}
		where += c
	}
	return where, args
}
