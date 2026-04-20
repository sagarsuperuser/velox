package subscription

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
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
	COALESCE(pending_plan_id,''), pending_plan_effective_at,
	current_billing_period_start, current_billing_period_end, next_billing_at,
	usage_cap_units, COALESCE(overage_action,'charge'),
	COALESCE(test_clock_id,''),
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
			usage_cap_units, overage_action, test_clock_id,
			created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,COALESCE(NULLIF($16,''),'charge'),NULLIF($17,''),$18,$18)
		RETURNING `+subCols,
		id, tenantID, sub.Code, sub.DisplayName, sub.CustomerID, sub.PlanID,
		sub.Status, sub.BillingTime, postgres.NullableTime(sub.TrialStartAt),
		postgres.NullableTime(sub.TrialEndAt), postgres.NullableTime(sub.StartedAt),
		postgres.NullableTime(sub.CurrentBillingPeriodStart),
		postgres.NullableTime(sub.CurrentBillingPeriodEnd),
		postgres.NullableTime(sub.NextBillingAt),
		sub.UsageCapUnits, sub.OverageAction, sub.TestClockID, now,
	).Scan(scanSubDest(&sub)...)

	if err != nil {
		switch postgres.UniqueViolationConstraint(err) {
		case "subscriptions_one_live_per_customer_plan":
			return domain.Subscription{}, errs.AlreadyExists("plan_id",
				fmt.Sprintf("customer already has an active or paused subscription on plan %q", sub.PlanID))
		case "":
			return domain.Subscription{}, err
		default:
			// Other 23505 (e.g. tenant_id, code) — map to code-conflict message.
			return domain.Subscription{}, errs.AlreadyExists("code",
				fmt.Sprintf("subscription code %q already exists", sub.Code))
		}
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

func (s *PostgresStore) List(ctx context.Context, filter ListFilter) ([]domain.Subscription, int, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, filter.TenantID)
	if err != nil {
		return nil, 0, err
	}
	defer postgres.Rollback(tx)

	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	where, args := buildSubWhere(filter)

	var total int
	countQuery := `SELECT COUNT(*) FROM subscriptions` + where
	if err := tx.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	query := `SELECT ` + subCols + ` FROM subscriptions` + where + ` ORDER BY created_at DESC LIMIT $` + fmt.Sprintf("%d", len(args)+1) + ` OFFSET $` + fmt.Sprintf("%d", len(args)+2)
	args = append(args, limit, filter.Offset)

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()

	var subs []domain.Subscription
	for rows.Next() {
		var sub domain.Subscription
		if err := rows.Scan(scanSubDest(&sub)...); err != nil {
			return nil, 0, err
		}
		subs = append(subs, sub)
	}
	return subs, total, rows.Err()
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
			pending_plan_id = $9, pending_plan_effective_at = $10,
			usage_cap_units = $11, overage_action = COALESCE(NULLIF($12,''),'charge'),
			updated_at = $13
		WHERE id = $14
		RETURNING `+subCols,
		sub.Status, postgres.NullableTime(sub.ActivatedAt), postgres.NullableTime(sub.CanceledAt),
		postgres.NullableTime(sub.TrialStartAt), postgres.NullableTime(sub.TrialEndAt),
		sub.PlanID, postgres.NullableString(sub.PreviousPlanID), postgres.NullableTime(sub.PlanChangedAt),
		postgres.NullableString(sub.PendingPlanID), postgres.NullableTime(sub.PendingPlanEffectiveAt),
		sub.UsageCapUnits, sub.OverageAction,
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

func (s *PostgresStore) PauseAtomic(ctx context.Context, tenantID, id string) (domain.Subscription, error) {
	return s.transitionAtomic(ctx, tenantID, id, transitionSpec{
		targetStatus: string(domain.SubscriptionPaused),
		allowedFrom:  []string{string(domain.SubscriptionActive)},
		wrongStateMsg: "can only pause active subscriptions, current status: %s",
	})
}

func (s *PostgresStore) ResumeAtomic(ctx context.Context, tenantID, id string) (domain.Subscription, error) {
	return s.transitionAtomic(ctx, tenantID, id, transitionSpec{
		targetStatus: string(domain.SubscriptionActive),
		allowedFrom:  []string{string(domain.SubscriptionPaused)},
		wrongStateMsg: "can only resume paused subscriptions, current status: %s",
	})
}

func (s *PostgresStore) CancelAtomic(ctx context.Context, tenantID, id string) (domain.Subscription, error) {
	return s.transitionAtomic(ctx, tenantID, id, transitionSpec{
		targetStatus: string(domain.SubscriptionCanceled),
		allowedFrom:  []string{string(domain.SubscriptionActive), string(domain.SubscriptionPaused)},
		setCanceledAt: true,
		wrongStateMsg: "can only cancel active or paused subscriptions, current status: %s",
	})
}

type transitionSpec struct {
	targetStatus  string
	allowedFrom   []string
	setCanceledAt bool
	wrongStateMsg string
}

func (s *PostgresStore) transitionAtomic(ctx context.Context, tenantID, id string, spec transitionSpec) (domain.Subscription, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Subscription{}, err
	}
	defer postgres.Rollback(tx)

	now := time.Now().UTC()

	// Build the WHERE status IN (...) clause with positional args starting at $3
	// ($1 = updated_at, $2 = id). canceled_at slots in at $3 when needed.
	canceledAtArg := "canceled_at"
	args := []any{now, id}
	argIdx := 3
	if spec.setCanceledAt {
		canceledAtArg = fmt.Sprintf("$%d", argIdx)
		args = append(args, now)
		argIdx++
	}
	statusPlaceholders := make([]string, len(spec.allowedFrom))
	for i, st := range spec.allowedFrom {
		statusPlaceholders[i] = fmt.Sprintf("$%d", argIdx)
		args = append(args, st)
		argIdx++
	}

	query := fmt.Sprintf(`
		UPDATE subscriptions
		SET status = '%s', canceled_at = %s, updated_at = $1
		WHERE id = $2 AND status IN (%s)
		RETURNING %s`,
		spec.targetStatus,
		canceledAtArg,
		strings.Join(statusPlaceholders, ","),
		subCols,
	)

	var sub domain.Subscription
	err = tx.QueryRowContext(ctx, query, args...).Scan(scanSubDest(&sub)...)
	if err == sql.ErrNoRows {
		// Row either doesn't exist or is in a disallowed status. Re-query to
		// distinguish and build a precise error.
		var currentStatus string
		err2 := tx.QueryRowContext(ctx, `SELECT status FROM subscriptions WHERE id = $1`, id).Scan(&currentStatus)
		if err2 == sql.ErrNoRows {
			return domain.Subscription{}, errs.ErrNotFound
		}
		if err2 != nil {
			return domain.Subscription{}, err2
		}
		return domain.Subscription{}, errs.InvalidState(fmt.Sprintf(spec.wrongStateMsg, currentStatus))
	}
	if err != nil {
		return domain.Subscription{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Subscription{}, err
	}
	return sub, nil
}

func (s *PostgresStore) SetPendingPlan(ctx context.Context, tenantID, id, pendingPlanID string, effectiveAt time.Time) (domain.Subscription, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Subscription{}, err
	}
	defer postgres.Rollback(tx)

	now := time.Now().UTC()
	var sub domain.Subscription
	err = tx.QueryRowContext(ctx, `
		UPDATE subscriptions
		SET pending_plan_id = $1, pending_plan_effective_at = $2, updated_at = $3
		WHERE id = $4
		RETURNING `+subCols,
		pendingPlanID, effectiveAt, now, id,
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

func (s *PostgresStore) ClearPendingPlan(ctx context.Context, tenantID, id string) (domain.Subscription, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Subscription{}, err
	}
	defer postgres.Rollback(tx)

	now := time.Now().UTC()
	var sub domain.Subscription
	err = tx.QueryRowContext(ctx, `
		UPDATE subscriptions
		SET pending_plan_id = NULL, pending_plan_effective_at = NULL, updated_at = $1
		WHERE id = $2
		RETURNING `+subCols,
		now, id,
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

func (s *PostgresStore) ApplyPendingPlanAtomic(ctx context.Context, tenantID, id string, now time.Time) (domain.Subscription, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Subscription{}, err
	}
	defer postgres.Rollback(tx)

	// Atomic swap: only succeeds if a due pending change still exists on this row.
	// previous_plan_id captures the plan being replaced; plan_changed_at marks the
	// swap instant. Both pending columns are cleared in the same statement so a
	// second invocation is a no-op (returns sql.ErrNoRows, surfaced as ErrNotFound
	// — callers must treat that as "already applied or canceled", not a hard error).
	var sub domain.Subscription
	err = tx.QueryRowContext(ctx, `
		UPDATE subscriptions
		SET previous_plan_id = plan_id,
		    plan_id = pending_plan_id,
		    plan_changed_at = $1,
		    pending_plan_id = NULL,
		    pending_plan_effective_at = NULL,
		    updated_at = $1
		WHERE id = $2
		  AND pending_plan_id IS NOT NULL
		  AND pending_plan_effective_at IS NOT NULL
		  AND pending_plan_effective_at <= $1
		RETURNING `+subCols,
		now, id,
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

	// Subscriptions attached to a test clock are "due" when their
	// next_billing_at is on-or-before the clock's frozen time, not wall clock.
	// LEFT JOIN keeps live subs (test_clock_id NULL) comparing against $1.
	// Columns must be qualified because test_clocks shares id/tenant_id/etc.
	rows, err := tx.QueryContext(ctx, `
		SELECT `+qualifiedSubCols("s")+` FROM subscriptions s
		LEFT JOIN test_clocks tc ON tc.id = s.test_clock_id
		WHERE s.status = 'active'
		  AND s.next_billing_at <= COALESCE(tc.frozen_time, $1)
		ORDER BY s.next_billing_at ASC LIMIT $2
		FOR UPDATE OF s SKIP LOCKED
	`, before, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

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

// qualifiedSubCols prefixes every column in subCols with the given table alias.
// Needed when subscriptions is JOINed against another table (e.g. test_clocks)
// with overlapping column names like id / tenant_id.
func qualifiedSubCols(alias string) string {
	var b strings.Builder
	for i, col := range strings.Split(subCols, ",") {
		if i > 0 {
			b.WriteString(", ")
		}
		col = strings.TrimSpace(col)
		if strings.HasPrefix(col, "COALESCE(") {
			// COALESCE(x,'') → COALESCE(alias.x,'')
			closing := strings.IndexByte(col, ')')
			inner := col[len("COALESCE(") : closing]
			parts := strings.SplitN(inner, ",", 2)
			b.WriteString("COALESCE(")
			b.WriteString(alias)
			b.WriteByte('.')
			b.WriteString(strings.TrimSpace(parts[0]))
			if len(parts) == 2 {
				b.WriteString(",")
				b.WriteString(parts[1])
			}
			b.WriteString(col[closing:])
			continue
		}
		b.WriteString(alias)
		b.WriteByte('.')
		b.WriteString(col)
	}
	return b.String()
}

func scanSubDest(s *domain.Subscription) []any {
	return []any{
		&s.ID, &s.TenantID, &s.Code, &s.DisplayName, &s.CustomerID, &s.PlanID,
		&s.Status, &s.BillingTime, &s.TrialStartAt, &s.TrialEndAt, &s.StartedAt,
		&s.ActivatedAt, &s.CanceledAt, &s.PreviousPlanID, &s.PlanChangedAt,
		&s.PendingPlanID, &s.PendingPlanEffectiveAt,
		&s.CurrentBillingPeriodStart,
		&s.CurrentBillingPeriodEnd, &s.NextBillingAt,
		&s.UsageCapUnits, &s.OverageAction,
		&s.TestClockID,
		&s.CreatedAt, &s.UpdatedAt,
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
