package subscription

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"

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

// ---------------------------------------------------------------------------
// Column sets
// ---------------------------------------------------------------------------

const subCols = `id, tenant_id, code, display_name, customer_id, status, billing_time,
	trial_start_at, trial_end_at, started_at, activated_at, canceled_at,
	cancel_at, COALESCE(cancel_at_period_end, false),
	pause_collection_behavior, pause_collection_resumes_at,
	billing_threshold_amount_gte, COALESCE(billing_threshold_reset_cycle, true),
	current_billing_period_start, current_billing_period_end, next_billing_at,
	usage_cap_units, COALESCE(overage_action,'charge'),
	COALESCE(test_clock_id,''),
	created_at, updated_at`

const itemCols = `id, tenant_id, subscription_id, plan_id, quantity, metadata,
	COALESCE(pending_plan_id,''), pending_plan_effective_at, plan_changed_at,
	created_at, updated_at`

// ---------------------------------------------------------------------------
// Subscription CRUD
// ---------------------------------------------------------------------------

func (s *PostgresStore) Create(ctx context.Context, tenantID string, sub domain.Subscription) (domain.Subscription, error) {
	if len(sub.Items) == 0 {
		return domain.Subscription{}, errs.Invalid("items", "a subscription must have at least one item")
	}

	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Subscription{}, err
	}
	defer postgres.Rollback(tx)

	id := postgres.NewID("vlx_sub")
	now := time.Now().UTC()

	err = scanSubRow(tx.QueryRowContext(ctx, `
		INSERT INTO subscriptions (id, tenant_id, code, display_name, customer_id, status,
			billing_time, trial_start_at, trial_end_at, started_at,
			current_billing_period_start, current_billing_period_end, next_billing_at,
			usage_cap_units, overage_action, test_clock_id,
			created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,COALESCE(NULLIF($15,''),'charge'),NULLIF($16,''),$17,$17)
		RETURNING `+subCols,
		id, tenantID, sub.Code, sub.DisplayName, sub.CustomerID,
		sub.Status, sub.BillingTime, postgres.NullableTime(sub.TrialStartAt),
		postgres.NullableTime(sub.TrialEndAt), postgres.NullableTime(sub.StartedAt),
		postgres.NullableTime(sub.CurrentBillingPeriodStart),
		postgres.NullableTime(sub.CurrentBillingPeriodEnd),
		postgres.NullableTime(sub.NextBillingAt),
		sub.UsageCapUnits, sub.OverageAction, sub.TestClockID, now,
	), &sub)

	if err != nil {
		if postgres.UniqueViolationConstraint(err) != "" {
			return domain.Subscription{}, errs.AlreadyExists("code",
				fmt.Sprintf("subscription code %q already exists", sub.Code))
		}
		return domain.Subscription{}, err
	}

	// Insert each requested item in the same tx. The UNIQUE (subscription_id,
	// plan_id) constraint rejects duplicate plans in the same request.
	inserted := make([]domain.SubscriptionItem, 0, len(sub.Items))
	for _, it := range sub.Items {
		qty := it.Quantity
		if qty <= 0 {
			qty = 1
		}
		var stored domain.SubscriptionItem
		err := tx.QueryRowContext(ctx, `
			INSERT INTO subscription_items (tenant_id, subscription_id, plan_id, quantity, metadata, created_at, updated_at)
			VALUES ($1,$2,$3,$4,COALESCE(NULLIF($5,'')::jsonb,'{}'::jsonb),$6,$6)
			RETURNING `+itemCols,
			tenantID, sub.ID, it.PlanID, qty, string(it.Metadata), now,
		).Scan(scanItemDest(&stored)...)
		if err != nil {
			if postgres.UniqueViolationConstraint(err) != "" {
				return domain.Subscription{}, errs.AlreadyExists("plan_id",
					fmt.Sprintf("duplicate plan %q in subscription items", it.PlanID))
			}
			return domain.Subscription{}, fmt.Errorf("insert item: %w", err)
		}
		inserted = append(inserted, stored)
	}
	sub.Items = inserted

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
	err = scanSubRow(tx.QueryRowContext(ctx, `SELECT `+subCols+` FROM subscriptions WHERE id = $1`, id), &sub)
	if err == sql.ErrNoRows {
		return domain.Subscription{}, errs.ErrNotFound
	}
	if err != nil {
		return domain.Subscription{}, err
	}

	if err := hydrateSubChildrenTx(ctx, tx, &sub); err != nil {
		return domain.Subscription{}, err
	}
	return sub, nil
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
	// Plan filter needs DISTINCT so a subscription with N items matching the
	// plan isn't counted N times. The JOIN is omitted (via buildSubWhere) when
	// PlanID isn't set, so the common list path still runs without the join.
	countQuery := `SELECT COUNT(DISTINCT s.id) FROM subscriptions s` + where
	if err := tx.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	query := `SELECT DISTINCT ` + qualifiedSubCols("s") + ` FROM subscriptions s` + where +
		` ORDER BY s.created_at DESC LIMIT $` + fmt.Sprintf("%d", len(args)+1) + ` OFFSET $` + fmt.Sprintf("%d", len(args)+2)
	args = append(args, limit, filter.Offset)

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()

	var subs []domain.Subscription
	for rows.Next() {
		var sub domain.Subscription
		if err := scanSubRow(rows, &sub); err != nil {
			return nil, 0, err
		}
		subs = append(subs, sub)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	// Hydrate items for each subscription. A second query per subscription is
	// acceptable on the list path at the default 50-row page size; if list
	// growth becomes hot, batch this into one IN() query.
	for i := range subs {
		if err := hydrateSubChildrenTx(ctx, tx, &subs[i]); err != nil {
			return nil, 0, err
		}
	}
	return subs, total, nil
}

func (s *PostgresStore) Update(ctx context.Context, tenantID string, sub domain.Subscription) (domain.Subscription, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Subscription{}, err
	}
	defer postgres.Rollback(tx)

	now := time.Now().UTC()
	err = scanSubRow(tx.QueryRowContext(ctx, `
		UPDATE subscriptions SET status = $1, activated_at = $2, canceled_at = $3,
			trial_start_at = $4, trial_end_at = $5,
			usage_cap_units = $6, overage_action = COALESCE(NULLIF($7,''),'charge'),
			updated_at = $8
		WHERE id = $9
		RETURNING `+subCols,
		sub.Status, postgres.NullableTime(sub.ActivatedAt), postgres.NullableTime(sub.CanceledAt),
		postgres.NullableTime(sub.TrialStartAt), postgres.NullableTime(sub.TrialEndAt),
		sub.UsageCapUnits, sub.OverageAction,
		now, sub.ID,
	), &sub)

	if err == sql.ErrNoRows {
		return domain.Subscription{}, errs.ErrNotFound
	}
	if err != nil {
		return domain.Subscription{}, err
	}

	if err := hydrateSubChildrenTx(ctx, tx, &sub); err != nil {
		return domain.Subscription{}, err
	}

	if err := tx.Commit(); err != nil {
		return domain.Subscription{}, err
	}
	return sub, nil
}

func (s *PostgresStore) PauseAtomic(ctx context.Context, tenantID, id string) (domain.Subscription, error) {
	return s.transitionAtomic(ctx, tenantID, id, transitionSpec{
		targetStatus:  string(domain.SubscriptionPaused),
		allowedFrom:   []string{string(domain.SubscriptionActive)},
		wrongStateMsg: "can only pause active subscriptions, current status: %s",
	})
}

func (s *PostgresStore) ResumeAtomic(ctx context.Context, tenantID, id string) (domain.Subscription, error) {
	return s.transitionAtomic(ctx, tenantID, id, transitionSpec{
		targetStatus:  string(domain.SubscriptionActive),
		allowedFrom:   []string{string(domain.SubscriptionPaused)},
		wrongStateMsg: "can only resume paused subscriptions, current status: %s",
	})
}

func (s *PostgresStore) CancelAtomic(ctx context.Context, tenantID, id string) (domain.Subscription, error) {
	return s.transitionAtomic(ctx, tenantID, id, transitionSpec{
		targetStatus:  string(domain.SubscriptionCanceled),
		allowedFrom:   []string{string(domain.SubscriptionActive), string(domain.SubscriptionPaused)},
		setCanceledAt: true,
		wrongStateMsg: "can only cancel active or paused subscriptions, current status: %s",
	})
}

// ScheduleCancellation persists the soft-cancel intent. Either field (or
// both) may be set; the row stores them and the billing cycle scan applies
// whichever boundary fires first. Returns the updated subscription with
// hydrated items so the handler can echo the same shape it returns
// elsewhere.
//
// The UPDATE is unconditional on status because callers can legitimately
// schedule a cancel against a paused subscription (Stripe allows the same).
// Only canceled/archived subs are rejected — there's nothing to schedule
// once the sub has already terminated.
func (s *PostgresStore) ScheduleCancellation(ctx context.Context, tenantID, id string, cancelAt *time.Time, cancelAtPeriodEnd bool) (domain.Subscription, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Subscription{}, err
	}
	defer postgres.Rollback(tx)

	now := time.Now().UTC()
	var sub domain.Subscription
	err = scanSubRow(tx.QueryRowContext(ctx, `
		UPDATE subscriptions
		SET cancel_at = $1, cancel_at_period_end = $2, updated_at = $3
		WHERE id = $4 AND status NOT IN ('canceled','archived')
		RETURNING `+subCols,
		postgres.NullableTime(cancelAt), cancelAtPeriodEnd, now, id,
	), &sub)
	if err == sql.ErrNoRows {
		var currentStatus string
		err2 := tx.QueryRowContext(ctx, `SELECT status FROM subscriptions WHERE id = $1`, id).Scan(&currentStatus)
		if err2 == sql.ErrNoRows {
			return domain.Subscription{}, errs.ErrNotFound
		}
		if err2 != nil {
			return domain.Subscription{}, err2
		}
		return domain.Subscription{}, errs.InvalidState(fmt.Sprintf("cannot schedule cancellation on %s subscription", currentStatus))
	}
	if err != nil {
		return domain.Subscription{}, err
	}

	if err := hydrateSubChildrenTx(ctx, tx, &sub); err != nil {
		return domain.Subscription{}, err
	}

	if err := tx.Commit(); err != nil {
		return domain.Subscription{}, err
	}
	return sub, nil
}

// ClearScheduledCancellation undoes a prior schedule. Idempotent — a row
// with both fields already cleared returns unchanged. Returns errs.ErrNotFound
// if the subscription doesn't exist; status is not checked because clearing
// a schedule on a canceled sub would be a no-op anyway and surfacing
// not-found there would mask the real "you already canceled" state.
func (s *PostgresStore) ClearScheduledCancellation(ctx context.Context, tenantID, id string) (domain.Subscription, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Subscription{}, err
	}
	defer postgres.Rollback(tx)

	now := time.Now().UTC()
	var sub domain.Subscription
	err = scanSubRow(tx.QueryRowContext(ctx, `
		UPDATE subscriptions
		SET cancel_at = NULL, cancel_at_period_end = false, updated_at = $1
		WHERE id = $2
		RETURNING `+subCols,
		now, id,
	), &sub)
	if err == sql.ErrNoRows {
		return domain.Subscription{}, errs.ErrNotFound
	}
	if err != nil {
		return domain.Subscription{}, err
	}

	if err := hydrateSubChildrenTx(ctx, tx, &sub); err != nil {
		return domain.Subscription{}, err
	}

	if err := tx.Commit(); err != nil {
		return domain.Subscription{}, err
	}
	return sub, nil
}

// FireScheduledCancellation transitions a subscription with a due cancel
// schedule to canceled in one statement. Differs from CancelAtomic in that
// (a) it accepts the engine's effectiveNow as the canceled_at timestamp so
// the audit trail stays consistent under test clocks, and (b) it clears
// the schedule fields so a subsequent cycle tick is a no-op rather than a
// confusing re-fire attempt. Returns errs.ErrNotFound if the row vanished
// or InvalidState if status was not active by the time the UPDATE ran (a
// concurrent immediate-cancel API call winning the race).
func (s *PostgresStore) FireScheduledCancellation(ctx context.Context, tenantID, id string, at time.Time) (domain.Subscription, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Subscription{}, err
	}
	defer postgres.Rollback(tx)

	var sub domain.Subscription
	err = scanSubRow(tx.QueryRowContext(ctx, `
		UPDATE subscriptions
		SET status = 'canceled',
		    canceled_at = $1,
		    cancel_at = NULL,
		    cancel_at_period_end = false,
		    updated_at = $1
		WHERE id = $2 AND status = 'active'
		RETURNING `+subCols,
		at, id,
	), &sub)
	if err == sql.ErrNoRows {
		var currentStatus string
		err2 := tx.QueryRowContext(ctx, `SELECT status FROM subscriptions WHERE id = $1`, id).Scan(&currentStatus)
		if err2 == sql.ErrNoRows {
			return domain.Subscription{}, errs.ErrNotFound
		}
		if err2 != nil {
			return domain.Subscription{}, err2
		}
		return domain.Subscription{}, errs.InvalidState(fmt.Sprintf("scheduled cancel cannot fire on %s subscription", currentStatus))
	}
	if err != nil {
		return domain.Subscription{}, err
	}

	if err := hydrateSubChildrenTx(ctx, tx, &sub); err != nil {
		return domain.Subscription{}, err
	}

	if err := tx.Commit(); err != nil {
		return domain.Subscription{}, err
	}
	return sub, nil
}

// SetPauseCollection writes (behavior, resumes_at) onto the row. Rejects
// rows in canceled/archived since collection-pause on a terminated sub has
// no meaning — the engine wouldn't observe the row anyway, but failing
// loudly here keeps the API honest. Active and paused (hard) are both
// allowed: a hard-paused sub can simultaneously have pause_collection
// configured for the moment status flips back to active.
func (s *PostgresStore) SetPauseCollection(ctx context.Context, tenantID, id string, pc domain.PauseCollection) (domain.Subscription, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Subscription{}, err
	}
	defer postgres.Rollback(tx)

	now := time.Now().UTC()
	var sub domain.Subscription
	err = scanSubRow(tx.QueryRowContext(ctx, `
		UPDATE subscriptions
		SET pause_collection_behavior = $1,
		    pause_collection_resumes_at = $2,
		    updated_at = $3
		WHERE id = $4 AND status NOT IN ('canceled','archived')
		RETURNING `+subCols,
		string(pc.Behavior), postgres.NullableTime(pc.ResumesAt), now, id,
	), &sub)
	if err == sql.ErrNoRows {
		var currentStatus string
		err2 := tx.QueryRowContext(ctx, `SELECT status FROM subscriptions WHERE id = $1`, id).Scan(&currentStatus)
		if err2 == sql.ErrNoRows {
			return domain.Subscription{}, errs.ErrNotFound
		}
		if err2 != nil {
			return domain.Subscription{}, err2
		}
		return domain.Subscription{}, errs.InvalidState(fmt.Sprintf("cannot pause collection on %s subscription", currentStatus))
	}
	if err != nil {
		return domain.Subscription{}, err
	}

	if err := hydrateSubChildrenTx(ctx, tx, &sub); err != nil {
		return domain.Subscription{}, err
	}

	if err := tx.Commit(); err != nil {
		return domain.Subscription{}, err
	}
	return sub, nil
}

// ClearPauseCollection nulls both pause_collection_* columns. Idempotent —
// runs even on a row that already has them null, returning the unchanged
// subscription. Returns errs.ErrNotFound if the row doesn't exist; status
// is not checked because clearing a no-op pause on a terminated sub is
// itself a no-op.
func (s *PostgresStore) ClearPauseCollection(ctx context.Context, tenantID, id string) (domain.Subscription, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Subscription{}, err
	}
	defer postgres.Rollback(tx)

	now := time.Now().UTC()
	var sub domain.Subscription
	err = scanSubRow(tx.QueryRowContext(ctx, `
		UPDATE subscriptions
		SET pause_collection_behavior = NULL,
		    pause_collection_resumes_at = NULL,
		    updated_at = $1
		WHERE id = $2
		RETURNING `+subCols,
		now, id,
	), &sub)
	if err == sql.ErrNoRows {
		return domain.Subscription{}, errs.ErrNotFound
	}
	if err != nil {
		return domain.Subscription{}, err
	}

	if err := hydrateSubChildrenTx(ctx, tx, &sub); err != nil {
		return domain.Subscription{}, err
	}

	if err := tx.Commit(); err != nil {
		return domain.Subscription{}, err
	}
	return sub, nil
}

// ActivateAfterTrial flips status 'trialing' → 'active' atomically. Sets
// activated_at = at if currently NULL (preserves the original activation
// timestamp on re-runs). Used by the billing engine when the trial window
// has elapsed during a cycle scan, and by the operator-facing EndTrial
// service action. Returns errs.InvalidState if the row's status was not
// 'trialing' at UPDATE time (e.g. it was already canceled or hard-paused);
// the caller distinguishes this from missing-row by querying current
// status when no row matches.
func (s *PostgresStore) ActivateAfterTrial(ctx context.Context, tenantID, id string, at time.Time) (domain.Subscription, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Subscription{}, err
	}
	defer postgres.Rollback(tx)

	var sub domain.Subscription
	err = scanSubRow(tx.QueryRowContext(ctx, `
		UPDATE subscriptions
		SET status = 'active',
		    activated_at = COALESCE(activated_at, $1),
		    updated_at = $1
		WHERE id = $2 AND status = 'trialing'
		RETURNING `+subCols,
		at, id,
	), &sub)
	if err == sql.ErrNoRows {
		var currentStatus string
		err2 := tx.QueryRowContext(ctx, `SELECT status FROM subscriptions WHERE id = $1`, id).Scan(&currentStatus)
		if err2 == sql.ErrNoRows {
			return domain.Subscription{}, errs.ErrNotFound
		}
		if err2 != nil {
			return domain.Subscription{}, err2
		}
		return domain.Subscription{}, errs.InvalidState(fmt.Sprintf("cannot end trial on %s subscription", currentStatus))
	}
	if err != nil {
		return domain.Subscription{}, err
	}

	if err := hydrateSubChildrenTx(ctx, tx, &sub); err != nil {
		return domain.Subscription{}, err
	}

	if err := tx.Commit(); err != nil {
		return domain.Subscription{}, err
	}
	return sub, nil
}

// ExtendTrial atomically updates trial_end_at on a 'trialing' row. The
// caller (Service.ExtendTrial) validates that newTrialEnd makes sense
// (in the future, after the existing trial_end_at). Returns
// errs.InvalidState if the row's status is not 'trialing' at UPDATE
// time — distinguishes operator-already-ended / hard-paused / canceled
// from missing-row by re-querying status when no row matches.
func (s *PostgresStore) ExtendTrial(ctx context.Context, tenantID, id string, newTrialEnd time.Time) (domain.Subscription, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Subscription{}, err
	}
	defer postgres.Rollback(tx)

	now := time.Now().UTC()
	var sub domain.Subscription
	err = scanSubRow(tx.QueryRowContext(ctx, `
		UPDATE subscriptions
		SET trial_end_at = $1, updated_at = $2
		WHERE id = $3 AND status = 'trialing'
		RETURNING `+subCols,
		newTrialEnd, now, id,
	), &sub)
	if err == sql.ErrNoRows {
		var currentStatus string
		err2 := tx.QueryRowContext(ctx, `SELECT status FROM subscriptions WHERE id = $1`, id).Scan(&currentStatus)
		if err2 == sql.ErrNoRows {
			return domain.Subscription{}, errs.ErrNotFound
		}
		if err2 != nil {
			return domain.Subscription{}, err2
		}
		return domain.Subscription{}, errs.InvalidState(fmt.Sprintf("cannot extend trial on %s subscription", currentStatus))
	}
	if err != nil {
		return domain.Subscription{}, err
	}

	if err := hydrateSubChildrenTx(ctx, tx, &sub); err != nil {
		return domain.Subscription{}, err
	}

	if err := tx.Commit(); err != nil {
		return domain.Subscription{}, err
	}
	return sub, nil
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
	err = scanSubRow(tx.QueryRowContext(ctx, query, args...), &sub)
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

	if err := hydrateSubChildrenTx(ctx, tx, &sub); err != nil {
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
	//
	// TxBypass is used to cross tenants (scheduler-wide sweep), but the
	// caller's ctx livemode must still scope us to a single partition —
	// otherwise the per-sub TxTenant calls downstream (plan / test-clock /
	// settings lookups) default to live and silently fail for test-mode
	// subs. The scheduler fans out ctx per livemode; we honour that here
	// with an explicit WHERE clause since TxBypass doesn't set app.livemode.
	rows, err := tx.QueryContext(ctx, `
		SELECT `+qualifiedSubCols("s")+` FROM subscriptions s
		LEFT JOIN test_clocks tc ON tc.id = s.test_clock_id
		WHERE s.status IN ('active', 'trialing')
		  AND s.livemode = $1
		  AND s.next_billing_at <= COALESCE(tc.frozen_time, $2)
		ORDER BY s.next_billing_at ASC LIMIT $3
		FOR UPDATE OF s SKIP LOCKED
	`, postgres.Livemode(ctx), before, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var subs []domain.Subscription
	for rows.Next() {
		var sub domain.Subscription
		if err := scanSubRow(rows, &sub); err != nil {
			return nil, err
		}
		subs = append(subs, sub)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i := range subs {
		if err := hydrateSubChildrenTx(ctx, tx, &subs[i]); err != nil {
			return nil, err
		}
	}
	return subs, nil
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

// ---------------------------------------------------------------------------
// Item CRUD
// ---------------------------------------------------------------------------

func (s *PostgresStore) ListItems(ctx context.Context, tenantID, subscriptionID string) ([]domain.SubscriptionItem, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)
	return listItemsTx(ctx, tx, subscriptionID)
}

func (s *PostgresStore) GetItem(ctx context.Context, tenantID, itemID string) (domain.SubscriptionItem, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.SubscriptionItem{}, err
	}
	defer postgres.Rollback(tx)

	var item domain.SubscriptionItem
	err = tx.QueryRowContext(ctx, `SELECT `+itemCols+` FROM subscription_items WHERE id = $1`, itemID).
		Scan(scanItemDest(&item)...)
	if err == sql.ErrNoRows {
		return domain.SubscriptionItem{}, errs.ErrNotFound
	}
	return item, err
}

func (s *PostgresStore) AddItem(ctx context.Context, tenantID string, item domain.SubscriptionItem) (domain.SubscriptionItem, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.SubscriptionItem{}, err
	}
	defer postgres.Rollback(tx)

	qty := item.Quantity
	if qty <= 0 {
		qty = 1
	}
	now := time.Now().UTC()
	var stored domain.SubscriptionItem
	err = tx.QueryRowContext(ctx, `
		INSERT INTO subscription_items (tenant_id, subscription_id, plan_id, quantity, metadata, created_at, updated_at)
		VALUES ($1,$2,$3,$4,COALESCE(NULLIF($5,'')::jsonb,'{}'::jsonb),$6,$6)
		RETURNING `+itemCols,
		tenantID, item.SubscriptionID, item.PlanID, qty, string(item.Metadata), now,
	).Scan(scanItemDest(&stored)...)
	if err != nil {
		if postgres.UniqueViolationConstraint(err) != "" {
			return domain.SubscriptionItem{}, errs.AlreadyExists("plan_id",
				fmt.Sprintf("subscription already has an item for plan %q", item.PlanID))
		}
		return domain.SubscriptionItem{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.SubscriptionItem{}, err
	}
	return stored, nil
}

func (s *PostgresStore) UpdateItemQuantity(ctx context.Context, tenantID, itemID string, quantity int64) (domain.SubscriptionItem, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.SubscriptionItem{}, err
	}
	defer postgres.Rollback(tx)

	now := time.Now().UTC()
	var stored domain.SubscriptionItem
	err = tx.QueryRowContext(ctx, `
		UPDATE subscription_items
		SET quantity = $1, updated_at = $2
		WHERE id = $3
		RETURNING `+itemCols,
		quantity, now, itemID,
	).Scan(scanItemDest(&stored)...)
	if err == sql.ErrNoRows {
		return domain.SubscriptionItem{}, errs.ErrNotFound
	}
	if err != nil {
		return domain.SubscriptionItem{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.SubscriptionItem{}, err
	}
	return stored, nil
}

func (s *PostgresStore) ApplyItemPlanImmediately(ctx context.Context, tenantID, itemID, newPlanID string, changedAt time.Time) (domain.SubscriptionItem, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.SubscriptionItem{}, err
	}
	defer postgres.Rollback(tx)

	// Clears any scheduled change — an immediate swap supersedes a pending one.
	var stored domain.SubscriptionItem
	err = tx.QueryRowContext(ctx, `
		UPDATE subscription_items
		SET plan_id = $1,
		    plan_changed_at = $2,
		    pending_plan_id = NULL,
		    pending_plan_effective_at = NULL,
		    updated_at = $2
		WHERE id = $3
		RETURNING `+itemCols,
		newPlanID, changedAt, itemID,
	).Scan(scanItemDest(&stored)...)
	if err == sql.ErrNoRows {
		return domain.SubscriptionItem{}, errs.ErrNotFound
	}
	if err != nil {
		if postgres.UniqueViolationConstraint(err) != "" {
			return domain.SubscriptionItem{}, errs.AlreadyExists("plan_id",
				fmt.Sprintf("subscription already has an item for plan %q", newPlanID))
		}
		return domain.SubscriptionItem{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.SubscriptionItem{}, err
	}
	return stored, nil
}

func (s *PostgresStore) SetItemPendingPlan(ctx context.Context, tenantID, itemID, pendingPlanID string, effectiveAt time.Time) (domain.SubscriptionItem, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.SubscriptionItem{}, err
	}
	defer postgres.Rollback(tx)

	now := time.Now().UTC()
	var stored domain.SubscriptionItem
	err = tx.QueryRowContext(ctx, `
		UPDATE subscription_items
		SET pending_plan_id = $1,
		    pending_plan_effective_at = $2,
		    updated_at = $3
		WHERE id = $4
		RETURNING `+itemCols,
		pendingPlanID, effectiveAt, now, itemID,
	).Scan(scanItemDest(&stored)...)
	if err == sql.ErrNoRows {
		return domain.SubscriptionItem{}, errs.ErrNotFound
	}
	if err != nil {
		return domain.SubscriptionItem{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.SubscriptionItem{}, err
	}
	return stored, nil
}

func (s *PostgresStore) ClearItemPendingPlan(ctx context.Context, tenantID, itemID string) (domain.SubscriptionItem, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.SubscriptionItem{}, err
	}
	defer postgres.Rollback(tx)

	now := time.Now().UTC()
	var stored domain.SubscriptionItem
	err = tx.QueryRowContext(ctx, `
		UPDATE subscription_items
		SET pending_plan_id = NULL,
		    pending_plan_effective_at = NULL,
		    updated_at = $1
		WHERE id = $2
		RETURNING `+itemCols,
		now, itemID,
	).Scan(scanItemDest(&stored)...)
	if err == sql.ErrNoRows {
		return domain.SubscriptionItem{}, errs.ErrNotFound
	}
	if err != nil {
		return domain.SubscriptionItem{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.SubscriptionItem{}, err
	}
	return stored, nil
}

func (s *PostgresStore) ApplyDuePendingItemPlansAtomic(ctx context.Context, tenantID, subscriptionID string, now time.Time) ([]domain.SubscriptionItem, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	// Swap every item under the subscription whose pending change is due.
	// All items move in one statement so a caller reading the result sees a
	// consistent snapshot. Other items (no pending change or pending-but-future)
	// are untouched.
	rows, err := tx.QueryContext(ctx, `
		UPDATE subscription_items
		SET plan_id = pending_plan_id,
		    plan_changed_at = $1,
		    pending_plan_id = NULL,
		    pending_plan_effective_at = NULL,
		    updated_at = $1
		WHERE subscription_id = $2
		  AND pending_plan_id IS NOT NULL
		  AND pending_plan_effective_at IS NOT NULL
		  AND pending_plan_effective_at <= $1
		RETURNING `+itemCols,
		now, subscriptionID,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var updated []domain.SubscriptionItem
	for rows.Next() {
		var it domain.SubscriptionItem
		if err := rows.Scan(scanItemDest(&it)...); err != nil {
			return nil, err
		}
		updated = append(updated, it)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return updated, nil
}

func (s *PostgresStore) RemoveItem(ctx context.Context, tenantID, itemID string) error {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return err
	}
	defer postgres.Rollback(tx)

	result, err := tx.ExecContext(ctx, `DELETE FROM subscription_items WHERE id = $1`, itemID)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return errs.ErrNotFound
	}
	return tx.Commit()
}

// ---------------------------------------------------------------------------
// Billing thresholds
// ---------------------------------------------------------------------------

// SetBillingThresholds writes the (amount_gte, reset_cycle, item_thresholds)
// triple onto the row in one transaction. Replaces the per-item threshold
// set: any item_thresholds row not in the new slice is deleted, present
// rows are upserted by primary key (subscription_item_id).
//
// Rejects rows in canceled/archived since a threshold on a terminal
// subscription has no meaning — the engine wouldn't observe it anyway,
// but the API surface should be consistent. Service layer is responsible
// for validating that every t.ItemThresholds[i].SubscriptionItemID
// belongs to this subscription.
func (s *PostgresStore) SetBillingThresholds(ctx context.Context, tenantID, id string, t domain.BillingThresholds) (domain.Subscription, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Subscription{}, err
	}
	defer postgres.Rollback(tx)

	now := time.Now().UTC()

	// Update the parent subscription columns in the same tx as the per-item
	// upserts so a partial commit can't leave amount_gte set without the
	// item rows the caller intended.
	var amountArg sql.NullInt64
	if t.AmountGTE > 0 {
		amountArg = sql.NullInt64{Int64: t.AmountGTE, Valid: true}
	}
	var sub domain.Subscription
	err = scanSubRow(tx.QueryRowContext(ctx, `
		UPDATE subscriptions
		SET billing_threshold_amount_gte = $1,
		    billing_threshold_reset_cycle = $2,
		    updated_at = $3
		WHERE id = $4 AND status NOT IN ('canceled','archived')
		RETURNING `+subCols,
		amountArg, t.ResetBillingCycle, now, id,
	), &sub)
	if err == sql.ErrNoRows {
		var currentStatus string
		err2 := tx.QueryRowContext(ctx, `SELECT status FROM subscriptions WHERE id = $1`, id).Scan(&currentStatus)
		if err2 == sql.ErrNoRows {
			return domain.Subscription{}, errs.ErrNotFound
		}
		if err2 != nil {
			return domain.Subscription{}, err2
		}
		return domain.Subscription{}, errs.InvalidState(fmt.Sprintf("cannot set billing threshold on %s subscription", currentStatus))
	}
	if err != nil {
		return domain.Subscription{}, err
	}

	// Replace the per-item set in one statement: DELETE everything not in the
	// new slice, then UPSERT each new row. This pattern preserves the
	// (subscription_item_id) PK across calls and lets idempotent re-set
	// converge to the same state without churn.
	keepIDs := make([]string, 0, len(t.ItemThresholds))
	for _, it := range t.ItemThresholds {
		keepIDs = append(keepIDs, it.SubscriptionItemID)
	}
	if len(keepIDs) > 0 {
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM subscription_item_thresholds
			WHERE subscription_id = $1 AND NOT (subscription_item_id = ANY($2::text[]))
		`, id, keepIDs); err != nil {
			return domain.Subscription{}, fmt.Errorf("delete stale item thresholds: %w", err)
		}
	} else {
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM subscription_item_thresholds WHERE subscription_id = $1
		`, id); err != nil {
			return domain.Subscription{}, fmt.Errorf("clear item thresholds: %w", err)
		}
	}

	for _, it := range t.ItemThresholds {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO subscription_item_thresholds (subscription_id, subscription_item_id, tenant_id, usage_gte, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $5)
			ON CONFLICT (subscription_item_id) DO UPDATE
			SET usage_gte = EXCLUDED.usage_gte,
			    updated_at = EXCLUDED.updated_at
		`, id, it.SubscriptionItemID, tenantID, it.UsageGTE.String(), now); err != nil {
			return domain.Subscription{}, fmt.Errorf("upsert item threshold for %s: %w", it.SubscriptionItemID, err)
		}
	}

	if err := hydrateSubChildrenTx(ctx, tx, &sub); err != nil {
		return domain.Subscription{}, err
	}

	if err := tx.Commit(); err != nil {
		return domain.Subscription{}, err
	}
	return sub, nil
}

// ClearBillingThresholds nulls the amount_gte column, resets reset_cycle
// to its default (true), and deletes every per-item threshold row for
// this subscription. Idempotent — clearing on a sub with no threshold
// returns the unchanged subscription.
func (s *PostgresStore) ClearBillingThresholds(ctx context.Context, tenantID, id string) (domain.Subscription, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Subscription{}, err
	}
	defer postgres.Rollback(tx)

	now := time.Now().UTC()
	var sub domain.Subscription
	err = scanSubRow(tx.QueryRowContext(ctx, `
		UPDATE subscriptions
		SET billing_threshold_amount_gte = NULL,
		    billing_threshold_reset_cycle = TRUE,
		    updated_at = $1
		WHERE id = $2
		RETURNING `+subCols,
		now, id,
	), &sub)
	if err == sql.ErrNoRows {
		return domain.Subscription{}, errs.ErrNotFound
	}
	if err != nil {
		return domain.Subscription{}, err
	}

	if _, err := tx.ExecContext(ctx, `
		DELETE FROM subscription_item_thresholds WHERE subscription_id = $1
	`, id); err != nil {
		return domain.Subscription{}, fmt.Errorf("clear item thresholds: %w", err)
	}

	// Reset BillingThresholds to nil since both amount and per-item are
	// now cleared. scanSubRow already left it nil (column became NULL),
	// but be explicit to keep hydrate from re-allocating an empty struct.
	sub.BillingThresholds = nil

	items, err := listItemsTx(ctx, tx, sub.ID)
	if err != nil {
		return domain.Subscription{}, err
	}
	sub.Items = items

	if err := tx.Commit(); err != nil {
		return domain.Subscription{}, err
	}
	return sub, nil
}

// ListWithThresholds returns active or trialing subscriptions in the
// given livemode partition that have at least one threshold configured
// (amount_gte set, OR at least one row in subscription_item_thresholds).
// Used by the threshold scan tick. Result is hydrated with items +
// thresholds so the caller can run the cycle math without a second
// round-trip per row.
//
// Uses TxBypass + explicit livemode filter for the same reason as
// GetDueBilling: the scheduler is cross-tenant and the per-tenant
// downstream calls (usage aggregation, plan reads) carry their own
// tenant context.
func (s *PostgresStore) ListWithThresholds(ctx context.Context, livemode bool, limit int) ([]domain.Subscription, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	if limit <= 0 {
		limit = 100
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT `+qualifiedSubCols("s")+` FROM subscriptions s
		WHERE s.status IN ('active', 'trialing')
		  AND s.livemode = $1
		  AND (s.billing_threshold_amount_gte IS NOT NULL
		       OR EXISTS (SELECT 1 FROM subscription_item_thresholds sit WHERE sit.subscription_id = s.id))
		ORDER BY s.id ASC
		LIMIT $2
	`, livemode, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var subs []domain.Subscription
	for rows.Next() {
		var sub domain.Subscription
		if err := scanSubRow(rows, &sub); err != nil {
			return nil, err
		}
		subs = append(subs, sub)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i := range subs {
		if err := hydrateSubChildrenTx(ctx, tx, &subs[i]); err != nil {
			return nil, err
		}
	}
	return subs, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// hydrateSubChildrenTx hydrates a subscription's items + billing thresholds
// inside a single transaction. Replaces the historic listItemsTx + manual
// item assignment pattern at most call sites — feature additions like
// thresholds should land here, not be duplicated at every read path.
//
// Threshold hydration only runs when the row has amount_gte set OR there
// is at least one row in subscription_item_thresholds. Pure no-threshold
// subs pay one extra LEFT JOIN'd EXISTS check, which is index-supported
// (idx_subscription_item_thresholds_subscription) and effectively free.
func hydrateSubChildrenTx(ctx context.Context, tx *sql.Tx, sub *domain.Subscription) error {
	items, err := listItemsTx(ctx, tx, sub.ID)
	if err != nil {
		return err
	}
	sub.Items = items
	return hydrateThresholds(ctx, tx, sub)
}

// listItemsTx reads a subscription's items inside an existing transaction so
// callers on the hot load path don't pay a second BEGIN/COMMIT. Returns items
// ordered by created_at so item display order stays stable across requests.
func listItemsTx(ctx context.Context, tx *sql.Tx, subscriptionID string) ([]domain.SubscriptionItem, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT `+itemCols+` FROM subscription_items
		WHERE subscription_id = $1
		ORDER BY created_at ASC, id ASC
	`, subscriptionID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var items []domain.SubscriptionItem
	for rows.Next() {
		var it domain.SubscriptionItem
		if err := rows.Scan(scanItemDest(&it)...); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

// qualifiedSubCols prefixes every column in subCols with the given table alias.
// Needed when subscriptions is JOINed against another table (e.g. test_clocks
// or subscription_items for filtering) with overlapping column names.
func qualifiedSubCols(alias string) string {
	var b strings.Builder
	for i, col := range splitTopLevelCommas(subCols) {
		if i > 0 {
			b.WriteString(", ")
		}
		col = strings.TrimSpace(col)
		if strings.HasPrefix(col, "COALESCE(") {
			closing := strings.IndexByte(col, ')')
			inner := col[len("COALESCE("):closing]
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

func splitTopLevelCommas(s string) []string {
	var out []string
	depth := 0
	start := 0
	for i, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}

// rowScanner abstracts *sql.Row and *sql.Rows so scanSubRow works for both
// QueryRowContext and the per-row loop in QueryContext.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanSubRow scans subCols into sub. Handles fields that need post-processing
// — currently the nullable (behavior, resumes_at) pair that composes into
// the *PauseCollection field on the domain struct, plus the (amount_gte,
// reset_cycle) pair that composes into BillingThresholds. Per-item
// thresholds are hydrated separately by hydrateThresholds since they
// live in an aux table.
func scanSubRow(row rowScanner, sub *domain.Subscription) error {
	var pauseBehavior sql.NullString
	var pauseResumesAt sql.NullTime
	var thresholdAmountGTE sql.NullInt64
	var thresholdResetCycle bool
	dest := []any{
		&sub.ID, &sub.TenantID, &sub.Code, &sub.DisplayName, &sub.CustomerID,
		&sub.Status, &sub.BillingTime, &sub.TrialStartAt, &sub.TrialEndAt, &sub.StartedAt,
		&sub.ActivatedAt, &sub.CanceledAt,
		&sub.CancelAt, &sub.CancelAtPeriodEnd,
		&pauseBehavior, &pauseResumesAt,
		&thresholdAmountGTE, &thresholdResetCycle,
		&sub.CurrentBillingPeriodStart,
		&sub.CurrentBillingPeriodEnd, &sub.NextBillingAt,
		&sub.UsageCapUnits, &sub.OverageAction,
		&sub.TestClockID,
		&sub.CreatedAt, &sub.UpdatedAt,
	}
	if err := row.Scan(dest...); err != nil {
		return err
	}
	if pauseBehavior.Valid {
		pc := &domain.PauseCollection{
			Behavior: domain.PauseCollectionBehavior(pauseBehavior.String),
		}
		if pauseResumesAt.Valid {
			t := pauseResumesAt.Time
			pc.ResumesAt = &t
		}
		sub.PauseCollection = pc
	}
	// BillingThresholds is partially populated here from the columns on the
	// row. ItemThresholds is filled in by hydrateThresholds because it lives
	// in an aux table. We always allocate the struct when amount_gte is set
	// or the row's reset_cycle has been explicitly toggled away from default,
	// and let hydrateThresholds add the items if any exist. When the caller
	// skips hydrateThresholds entirely (rare — see ListWithThresholds for the
	// only such site), the struct is nil-or-amount-only, which is the same
	// shape pre-hydrate.
	if thresholdAmountGTE.Valid {
		sub.BillingThresholds = &domain.BillingThresholds{
			AmountGTE:         thresholdAmountGTE.Int64,
			ResetBillingCycle: thresholdResetCycle,
			ItemThresholds:    []domain.SubscriptionItemThreshold{},
		}
	}
	return nil
}

// hydrateThresholds fills sub.BillingThresholds.ItemThresholds from the
// aux table. Called after scanSubRow on a hot read path (Get, List, the
// scan tick) when the caller wants the full threshold view. Two cases:
//
//   - sub.BillingThresholds is non-nil from scanSubRow because amount_gte
//     was set: append item rows (if any) to the existing slice.
//
//   - sub.BillingThresholds is nil because amount_gte was NULL: if any
//     item rows exist, allocate a new struct with the aux rows; otherwise
//     leave nil.
//
// Empty aux rows + NULL amount_gte means no thresholds — sub stays nil.
func hydrateThresholds(ctx context.Context, tx *sql.Tx, sub *domain.Subscription) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT subscription_item_id, usage_gte
		FROM subscription_item_thresholds
		WHERE subscription_id = $1
		ORDER BY subscription_item_id ASC
	`, sub.ID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()

	var items []domain.SubscriptionItemThreshold
	for rows.Next() {
		var t domain.SubscriptionItemThreshold
		var usageGTE string
		if err := rows.Scan(&t.SubscriptionItemID, &usageGTE); err != nil {
			return err
		}
		dec, err := decimal.NewFromString(usageGTE)
		if err != nil {
			return fmt.Errorf("parse usage_gte for item %s: %w", t.SubscriptionItemID, err)
		}
		t.UsageGTE = dec
		items = append(items, t)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(items) == 0 {
		// No per-item thresholds. Leave existing struct as-is (which may
		// have an amount-only threshold from scanSubRow), or nil.
		return nil
	}
	if sub.BillingThresholds == nil {
		sub.BillingThresholds = &domain.BillingThresholds{
			ResetBillingCycle: true, // default mirrors the column default
			ItemThresholds:    items,
		}
		return nil
	}
	sub.BillingThresholds.ItemThresholds = items
	return nil
}

func scanItemDest(it *domain.SubscriptionItem) []any {
	return []any{
		&it.ID, &it.TenantID, &it.SubscriptionID, &it.PlanID, &it.Quantity, &it.Metadata,
		&it.PendingPlanID, &it.PendingPlanEffectiveAt, &it.PlanChangedAt,
		&it.CreatedAt, &it.UpdatedAt,
	}
}

func buildSubWhere(f ListFilter) (string, []any) {
	var clauses []string
	var args []any
	idx := 1

	// Joining items only when PlanID is set keeps the common list path
	// (no plan filter) off the join entirely.
	hasPlanFilter := f.PlanID != ""

	if f.CustomerID != "" {
		clauses = append(clauses, fmt.Sprintf("s.customer_id = $%d", idx))
		args = append(args, f.CustomerID)
		idx++
	}
	if hasPlanFilter {
		clauses = append(clauses, fmt.Sprintf("si.plan_id = $%d", idx))
		args = append(args, f.PlanID)
		idx++
	}
	if f.Status != "" {
		clauses = append(clauses, fmt.Sprintf("s.status = $%d", idx))
		args = append(args, f.Status)
	}

	prefix := ""
	if hasPlanFilter {
		prefix = " JOIN subscription_items si ON si.subscription_id = s.id"
	}

	if len(clauses) == 0 {
		return prefix, args
	}
	where := prefix + " WHERE "
	for i, c := range clauses {
		if i > 0 {
			where += " AND "
		}
		where += c
	}
	return where, args
}

// Subscription scan now includes PlanChangedAt on items, but the bytes
// exchange via JSON. pgx returns bytea for JSONB by default; store/consume
// raw bytes on the Metadata field so the caller owns the encoding policy.
var _ = sql.ErrNoRows // retain import
