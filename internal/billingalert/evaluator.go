package billingalert

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/shopspring/decimal"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/platform/clock"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

// Evaluator scans active billing alerts on a periodic tick, evaluates
// each against the customer's current billing cycle, and fires a
// `billing.alert.triggered` webhook + state mutation atomically.
//
// One leader per cluster runs the tick — gated by the
// LockKeyBillingAlertEvaluator advisory lock. Followers skip silently
// (the lock auto-releases on the leader's TCP session drop, so crashes
// don't strand the lock).
//
// Per-alert work runs inside a tenant-scoped tx: the trigger row
// insert, the alert status update, and the outbox enqueue all commit
// together — see docs/design-billing-alerts.md "Atomicity contract".
type Evaluator struct {
	store         Store
	customers     CustomerLookup
	subscriptions SubscriptionLister
	pricing       PricingReader
	usage         UsageAggregator
	outbox        OutboxEnqueuer
	locker        Locker
	lockKey       int64
	interval      time.Duration
	batch         int
	clock         clock.Clock
	onTick        func()
}

// NewEvaluator wires the composition. interval defaults to 1 minute
// if zero; batch defaults to 500 alerts per tick (the partial index
// keeps this scan cheap, but cap to bound an outlier tenant). All
// collaborators are required.
func NewEvaluator(
	store Store,
	customers CustomerLookup,
	subscriptions SubscriptionLister,
	pricing PricingReader,
	usage UsageAggregator,
	outbox OutboxEnqueuer,
	clk clock.Clock,
) *Evaluator {
	if clk == nil {
		clk = clock.Real()
	}
	return &Evaluator{
		store:         store,
		customers:     customers,
		subscriptions: subscriptions,
		pricing:       pricing,
		usage:         usage,
		outbox:        outbox,
		clock:         clk,
		interval:      1 * time.Minute,
		batch:         500,
	}
}

// SetInterval overrides the default tick interval. Operators can tune
// via VELOX_BILLING_ALERTS_INTERVAL on the wire side.
func (e *Evaluator) SetInterval(d time.Duration) {
	if d > 0 {
		e.interval = d
	}
}

// SetBatch overrides the default per-tick batch size.
func (e *Evaluator) SetBatch(n int) {
	if n > 0 {
		e.batch = n
	}
}

// SetLocker enables leader gating. When set, the evaluator only runs
// the tick if it wins the advisory lock — preventing two replicas
// from double-firing alerts.
func (e *Evaluator) SetLocker(locker Locker, lockKey int64) {
	e.locker = locker
	e.lockKey = lockKey
}

// SetOnTick registers a callback invoked after each completed tick.
// Used by the API health check to track liveness.
func (e *Evaluator) SetOnTick(fn func()) {
	e.onTick = fn
}

// Start runs the evaluator in a background goroutine. Blocks until
// ctx is cancelled (graceful shutdown).
func (e *Evaluator) Start(ctx context.Context) {
	slog.Info("billing alerts evaluator started", "interval", e.interval.String(), "batch_size", e.batch)
	ticker := time.NewTicker(e.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("billing alerts evaluator stopped")
			return
		case <-ticker.C:
			e.RunOnce(ctx)
		}
	}
}

// RunOnce performs one evaluator tick. Exposed for tests + manual
// trigger. Under leader gating, returns immediately when another
// replica holds the lock.
func (e *Evaluator) RunOnce(ctx context.Context) {
	if e.locker != nil && e.lockKey != 0 {
		lock, ok, err := e.locker.TryAdvisoryLock(ctx, e.lockKey)
		if err != nil {
			slog.Error("billing alerts evaluator: lock acquire failed", "error", err)
			return
		}
		if !ok {
			slog.Debug("billing alerts evaluator: another leader holds the lock; skipping tick")
			return
		}
		defer lock.Release()
	}

	// Mode fan-out: live then test. RLS partitions on (tenant_id,
	// livemode) downstream — without WithLivemode the per-alert tx
	// would silently default to live mode and miss test-mode rows.
	for _, live := range []bool{true, false} {
		modeCtx := postgres.WithLivemode(ctx, live)
		e.runForMode(modeCtx, live)
	}

	if e.onTick != nil {
		e.onTick()
	}
}

func (e *Evaluator) runForMode(ctx context.Context, live bool) {
	mode := "live"
	if !live {
		mode = "test"
	}

	alerts, err := e.store.ListCandidates(ctx, e.batch)
	if err != nil {
		slog.Error("billing alerts evaluator: list candidates", "mode", mode, "error", err)
		return
	}

	for _, alert := range alerts {
		if err := e.evaluateAlert(ctx, alert); err != nil {
			slog.Error("billing alerts evaluator: per-alert error",
				"mode", mode,
				"alert_id", alert.ID,
				"tenant_id", alert.TenantID,
				"error", err,
			)
		}
	}
}

// evaluateAlert resolves the customer's current cycle, evaluates the
// threshold, and fires (with full atomicity) when crossed.
func (e *Evaluator) evaluateAlert(ctx context.Context, alert domain.BillingAlert) error {
	// Resolve customer + primary active subscription. A customer
	// without an active sub can't have a current cycle; skip with a
	// debug log (the alert stays armed in case the customer
	// resubscribes later).
	if _, err := e.customers.Get(ctx, alert.TenantID, alert.CustomerID); err != nil {
		// Customer deletion cascades to alerts via FK, so reaching
		// this branch means the customer became unreadable mid-tick
		// (e.g. race with archive). Log and move on.
		return fmt.Errorf("customer %s: %w", alert.CustomerID, err)
	}
	subs, _, err := e.subscriptions.List(ctx, SubscriptionListFilter{
		TenantID:   alert.TenantID,
		CustomerID: alert.CustomerID,
	})
	if err != nil {
		return fmt.Errorf("list subscriptions: %w", err)
	}
	primary, ok := pickPrimarySubscription(subs)
	if !ok {
		slog.Debug("billing alerts: customer has no active subscription with current cycle; skipping",
			"alert_id", alert.ID, "customer_id", alert.CustomerID)
		return nil
	}

	from := *primary.CurrentBillingPeriodStart
	to := *primary.CurrentBillingPeriodEnd

	// Per-period rearm: alert fired in a previous cycle and the
	// customer's cycle has rolled forward. Flip back to active and
	// re-evaluate against the new cycle in this same tick (so the
	// operator gets a fresh signal as soon as the threshold crosses
	// in the new cycle, not on the next tick).
	if alert.Status == domain.BillingAlertStatusTriggeredForPeriod {
		if alert.LastPeriodStart == nil || from.After(*alert.LastPeriodStart) {
			if err := e.store.Rearm(ctx, alert.TenantID, alert.ID); err != nil {
				return fmt.Errorf("rearm: %w", err)
			}
			alert.Status = domain.BillingAlertStatusActive
			alert.LastPeriodStart = nil
		} else {
			// Still within the cycle the alert already fired for;
			// nothing to do until rollover.
			return nil
		}
	}

	// Rate the alert's scope. Two paths:
	//   - meter_id set → evaluate that one meter's per-rule resolution
	//     (filtered by dimensions if present).
	//   - meter_id empty → walk the customer's plan(s) and sum across
	//     every meter (no dimension filter applies in this path).
	observedAmount, observedQty, currency, err := e.observeUsage(ctx, alert, primary, from, to)
	if err != nil {
		return fmt.Errorf("observe usage: %w", err)
	}

	if !shouldFire(alert.Threshold, observedAmount, observedQty) {
		return nil
	}

	// Fire. Open the tenant-scoped tx, insert the trigger row + flip
	// the alert status (FireInTx), enqueue the outbox row, commit. A
	// crash mid-tx rolls back everything; the next tick re-evaluates
	// and the UNIQUE constraint guarantees no double-emit on retry.
	tx, err := e.store.BeginTenantTx(ctx, alert.TenantID)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer postgres.Rollback(tx)

	newStatus := domain.BillingAlertStatusTriggered
	if alert.Recurrence == domain.BillingAlertRecurrencePerPeriod {
		newStatus = domain.BillingAlertStatusTriggeredForPeriod
	}

	trigger := domain.BillingAlertTrigger{
		TenantID:            alert.TenantID,
		AlertID:             alert.ID,
		PeriodFrom:          from,
		PeriodTo:            to,
		ObservedAmountCents: observedAmount,
		ObservedQuantity:    observedQty,
		Currency:            currency,
	}

	inserted, err := e.store.FireInTx(ctx, tx, alert, trigger, newStatus)
	if err != nil {
		if errors.Is(err, ErrAlreadyFired) {
			// Another evaluator already fired this period (shouldn't
			// happen under leader gating, but the UNIQUE constraint
			// is the safety net). No-op.
			return nil
		}
		return fmt.Errorf("fire: %w", err)
	}

	payload := buildEventPayload(alert, inserted)
	if _, err := e.outbox.Enqueue(ctx, tx, alert.TenantID, domain.EventBillingAlertTriggered, payload); err != nil {
		return fmt.Errorf("enqueue outbox: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	slog.Info("billing alert fired",
		"alert_id", alert.ID,
		"tenant_id", alert.TenantID,
		"customer_id", alert.CustomerID,
		"observed_amount_cents", observedAmount,
		"recurrence", string(alert.Recurrence),
	)
	return nil
}

// observeUsage rates the alert's scope and returns
// (amount_cents, quantity, currency). The two paths diverge based on
// whether filter.meter_id is set:
//
//   - meter scoped: walk the rules for that meter, filter the rule
//     aggregations by the alert's dimension filter (strict-superset
//     match against rule dimension_match), rate each rule, sum.
//   - cross-meter: walk every meter on the customer's plan(s), rate
//     each meter's resolved usage, sum amounts across all meters.
//     dimension_match doesn't apply on this path (cross-meter
//     dimension filtering rejected at create time).
func (e *Evaluator) observeUsage(
	ctx context.Context,
	alert domain.BillingAlert,
	sub domain.Subscription,
	from, to time.Time,
) (int64, decimal.Decimal, string, error) {
	if alert.Filter.MeterID != "" {
		return e.observeMeterScoped(ctx, alert, from, to)
	}
	return e.observeCrossMeter(ctx, alert, sub, from, to)
}

// observeMeterScoped rates a single meter through the priority+claim
// path, filters per-rule aggregations by the alert's dimension filter,
// and sums amount_cents + quantity across the matching rule buckets.
func (e *Evaluator) observeMeterScoped(
	ctx context.Context,
	alert domain.BillingAlert,
	from, to time.Time,
) (int64, decimal.Decimal, string, error) {
	meter, err := e.pricing.GetMeter(ctx, alert.TenantID, alert.Filter.MeterID)
	if err != nil {
		return 0, decimal.Zero, "", fmt.Errorf("get meter: %w", err)
	}

	defaultMode := mapMeterAggregation(meter.Aggregation)
	aggs, err := e.usage.AggregateByPricingRules(ctx, alert.TenantID, alert.CustomerID, alert.Filter.MeterID, defaultMode, from, to)
	if err != nil {
		return 0, decimal.Zero, "", fmt.Errorf("aggregate by rules: %w", err)
	}

	rules, err := e.pricing.ListMeterPricingRulesByMeter(ctx, alert.TenantID, alert.Filter.MeterID)
	if err != nil {
		return 0, decimal.Zero, "", fmt.Errorf("list pricing rules: %w", err)
	}
	rulesByID := make(map[string]domain.MeterPricingRule, len(rules))
	for _, rule := range rules {
		rulesByID[rule.ID] = rule
	}

	var (
		totalCents int64
		totalQty   = decimal.Zero
		currency   string
	)

	for _, agg := range aggs {
		// Dimension filter: strict-superset match against rule's
		// dimension_match. Empty filter matches every rule.
		if len(alert.Filter.Dimensions) > 0 {
			rule, ok := rulesByID[agg.RuleID]
			if !ok || !dimensionsMatch(alert.Filter.Dimensions, rule.DimensionMatch) {
				continue
			}
		}

		ratingRuleID := agg.RatingRuleVersionID
		if ratingRuleID == "" {
			ratingRuleID = meter.RatingRuleVersionID
		}
		if ratingRuleID == "" {
			// No rule binding — skip but don't fail; the alert
			// stays armed.
			continue
		}
		ratingRule, err := e.pricing.GetRatingRule(ctx, alert.TenantID, ratingRuleID)
		if err != nil {
			return 0, decimal.Zero, "", fmt.Errorf("get rating rule: %w", err)
		}

		cents, err := domain.ComputeAmountCents(ratingRule, agg.Quantity)
		if err != nil {
			return 0, decimal.Zero, "", fmt.Errorf("compute amount: %w", err)
		}

		if currency == "" {
			currency = ratingRule.Currency
		}
		totalCents += cents
		totalQty = totalQty.Add(agg.Quantity)
	}

	return totalCents, totalQty, currency, nil
}

// observeCrossMeter walks the customer's plan(s) and aggregates amount
// across every meter. Quantity is unmeaningful in this path (different
// meters can have different units), so we return zero — the threshold
// validation already rejected `usage_gte` for cross-meter alerts.
func (e *Evaluator) observeCrossMeter(
	ctx context.Context,
	alert domain.BillingAlert,
	sub domain.Subscription,
	from, to time.Time,
) (int64, decimal.Decimal, string, error) {
	plans := map[string]domain.Plan{}
	seen := map[string]struct{}{}
	var meterIDs []string
	for _, item := range sub.Items {
		plan, ok := plans[item.PlanID]
		if !ok {
			p, err := e.pricing.GetPlan(ctx, alert.TenantID, item.PlanID)
			if err != nil {
				return 0, decimal.Zero, "", fmt.Errorf("get plan: %w", err)
			}
			plan = p
			plans[item.PlanID] = plan
		}
		for _, mid := range plan.MeterIDs {
			if _, dup := seen[mid]; dup {
				continue
			}
			seen[mid] = struct{}{}
			meterIDs = append(meterIDs, mid)
		}
	}

	var (
		totalCents int64
		currency   string
	)
	for _, meterID := range meterIDs {
		meter, err := e.pricing.GetMeter(ctx, alert.TenantID, meterID)
		if err != nil {
			return 0, decimal.Zero, "", fmt.Errorf("get meter %s: %w", meterID, err)
		}
		defaultMode := mapMeterAggregation(meter.Aggregation)
		aggs, err := e.usage.AggregateByPricingRules(ctx, alert.TenantID, alert.CustomerID, meterID, defaultMode, from, to)
		if err != nil {
			return 0, decimal.Zero, "", fmt.Errorf("aggregate %s: %w", meterID, err)
		}
		for _, agg := range aggs {
			ratingRuleID := agg.RatingRuleVersionID
			if ratingRuleID == "" {
				ratingRuleID = meter.RatingRuleVersionID
			}
			if ratingRuleID == "" {
				continue
			}
			ratingRule, err := e.pricing.GetRatingRule(ctx, alert.TenantID, ratingRuleID)
			if err != nil {
				return 0, decimal.Zero, "", fmt.Errorf("get rating rule: %w", err)
			}
			cents, err := domain.ComputeAmountCents(ratingRule, agg.Quantity)
			if err != nil {
				return 0, decimal.Zero, "", fmt.Errorf("compute amount: %w", err)
			}
			if currency == "" {
				currency = ratingRule.Currency
			}
			totalCents += cents
		}
	}

	return totalCents, decimal.Zero, currency, nil
}

// shouldFire compares the observed values against the alert's
// threshold. Exactly one of AmountCentsGTE / QuantityGTE is set
// (DB CHECK + service validation); compare against the populated one.
func shouldFire(threshold domain.BillingAlertThreshold, observedAmount int64, observedQty decimal.Decimal) bool {
	if threshold.AmountCentsGTE != nil {
		return observedAmount >= *threshold.AmountCentsGTE
	}
	if threshold.QuantityGTE != nil {
		return observedQty.GreaterThanOrEqual(*threshold.QuantityGTE)
	}
	return false
}

// dimensionsMatch returns true when every key/value pair in `filter`
// is present and equal in `actual` (strict-superset semantics —
// `actual` is a superset of `filter`). An empty filter matches every
// actual map.
func dimensionsMatch(filter, actual map[string]any) bool {
	if len(filter) == 0 {
		return true
	}
	for k, v := range filter {
		got, ok := actual[k]
		if !ok {
			return false
		}
		if !equalAny(v, got) {
			return false
		}
	}
	return true
}

// equalAny compares two scalar values for equality. JSON unmarshaling
// produces float64 for all numbers, so we normalise int → float64
// before comparing — otherwise an alert filter `{count:5}` would
// never match an event with `{count:5.0}` (same value, different
// concrete type).
func equalAny(a, b any) bool {
	switch av := a.(type) {
	case nil:
		return b == nil
	case bool:
		bv, ok := b.(bool)
		return ok && bv == av
	case string:
		bv, ok := b.(string)
		return ok && bv == av
	case float64:
		return numericEqual(av, b)
	case float32:
		return numericEqual(float64(av), b)
	case int:
		return numericEqual(float64(av), b)
	case int32:
		return numericEqual(float64(av), b)
	case int64:
		return numericEqual(float64(av), b)
	}
	return false
}

func numericEqual(a float64, b any) bool {
	switch bv := b.(type) {
	case float64:
		return a == bv
	case float32:
		return a == float64(bv)
	case int:
		return a == float64(bv)
	case int32:
		return a == float64(bv)
	case int64:
		return a == float64(bv)
	}
	return false
}

// pickPrimarySubscription mirrors the customer-usage / create_preview
// heuristic: filter to active or trialing, pick the most recent
// current_period_start. Subs without a current cycle (paused,
// canceled, draft) are excluded — they have no cycle to evaluate.
func pickPrimarySubscription(subs []domain.Subscription) (domain.Subscription, bool) {
	var primary *domain.Subscription
	for i := range subs {
		sub := &subs[i]
		if sub.Status != domain.SubscriptionActive && sub.Status != domain.SubscriptionTrialing {
			continue
		}
		if sub.CurrentBillingPeriodStart == nil || sub.CurrentBillingPeriodEnd == nil {
			continue
		}
		if primary == nil || sub.CurrentBillingPeriodStart.After(*primary.CurrentBillingPeriodStart) {
			primary = sub
		}
	}
	if primary == nil {
		return domain.Subscription{}, false
	}
	return *primary, true
}

// mapMeterAggregation translates the meter's stored aggregation
// string to the AggregationMode the priority+claim resolver accepts.
// Duplicated from billing.previewMeter / usage.customer_usage rather
// than imported — same rationale: keep cross-domain imports flat.
func mapMeterAggregation(agg string) domain.AggregationMode {
	switch agg {
	case "sum":
		return domain.AggSum
	case "count":
		return domain.AggCount
	case "max":
		return domain.AggMax
	case "last":
		return domain.AggLastDuringPeriod
	default:
		return domain.AggSum
	}
}

// buildEventPayload is the single source of truth for the
// `billing.alert.triggered` webhook payload shape. Snake-case
// throughout; always-object idiom on `filter.dimensions`; both
// threshold fields present (one as null); decimal as JSON string.
//
// See docs/design-billing-alerts.md "Webhook payload" for the spec.
func buildEventPayload(alert domain.BillingAlert, trigger domain.BillingAlertTrigger) map[string]any {
	dims := alert.Filter.Dimensions
	if dims == nil {
		dims = map[string]any{}
	}

	threshold := map[string]any{
		"amount_gte": nil,
		"usage_gte":  nil,
	}
	if alert.Threshold.AmountCentsGTE != nil {
		threshold["amount_gte"] = *alert.Threshold.AmountCentsGTE
	}
	if alert.Threshold.QuantityGTE != nil {
		threshold["usage_gte"] = alert.Threshold.QuantityGTE.String()
	}

	return map[string]any{
		"alert_id":    alert.ID,
		"customer_id": alert.CustomerID,
		"title":       alert.Title,
		"threshold":   threshold,
		"observed": map[string]any{
			"amount_cents": trigger.ObservedAmountCents,
			"quantity":     trigger.ObservedQuantity.String(),
		},
		"currency":     trigger.Currency,
		"triggered_at": trigger.TriggeredAt.UTC().Format(time.RFC3339Nano),
		"period": map[string]any{
			"from":   trigger.PeriodFrom.UTC().Format(time.RFC3339Nano),
			"to":     trigger.PeriodTo.UTC().Format(time.RFC3339Nano),
			"source": "current_billing_cycle",
		},
		"filter": map[string]any{
			"meter_id":   alert.Filter.MeterID,
			"dimensions": dims,
		},
		"recurrence": string(alert.Recurrence),
	}
}
