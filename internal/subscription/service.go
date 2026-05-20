package subscription

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/clock"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

var slugPattern = regexp.MustCompile(`^[a-zA-Z0-9_\-]+$`)

// SettingsReader is the narrow surface this service uses to resolve
// the tenant's timezone for period-boundary snapping. Optional —
// nil-safe, falls back to UTC. Avoids importing internal/tenant
// directly so the dependency graph stays acyclic.
type SettingsReader interface {
	Get(ctx context.Context, tenantID string) (domain.TenantSettings, error)
}

// CustomerReader fetches a customer by id — narrow shape used by
// Create to read customer.test_clock_id for inheritance (ADR-027).
// Implemented by *customer.PostgresStore.
type CustomerReader interface {
	Get(ctx context.Context, tenantID, customerID string) (domain.Customer, error)
}

// Biller is the narrow shape used after Create + Cancel to handle
// the in_advance bill_timing artifacts (ADR-031): day-1 invoice on
// create, cancel proration credit on mid-period cancel. Optional —
// nil-safe; without it, in_advance subs silently behave like
// in_arrears (no day-1 invoice, no cancel proration). Implemented
// by *billing.Engine.
type Biller interface {
	BillOnCreate(ctx context.Context, sub domain.Subscription) (domain.Invoice, error)
	// BillFinalOnImmediateCancel emits the final partial-period invoice
	// for a mid-period immediate cancel (in_arrears prorated base +
	// usage). No-op when canceled_at is at/after current_period_end
	// (clean cancel, cycle close handles it).
	BillFinalOnImmediateCancel(ctx context.Context, sub domain.Subscription) (domain.Invoice, error)
	BillOnCancel(ctx context.Context, sub domain.Subscription) (int64, error)
}

// PlanReader (defined in handler.go) is also used at sub-lifecycle
// entry points (Create / Activate / EndTrial / ExtendTrial) to read
// BillingInterval for yearly-aware period anchoring (Bug #10).
// Optional: nil-safe — the period helpers default to monthly math
// when the reader isn't wired.

type Service struct {
	store     Store
	clock     clock.Clock
	settings  SettingsReader
	customers CustomerReader
	plans     PlanReader
	biller    Biller
	resolver  clock.Resolver
	events    domain.EventDispatcher
}

func NewService(store Store, clk clock.Clock) *Service {
	if clk == nil {
		clk = clock.Real()
	}
	return &Service{store: store, clock: clk}
}

// SetSettingsReader wires the tenant settings reader. Kept as a setter
// rather than a constructor arg because router.go builds the settings
// store after the subscription service today; setter avoids a forced
// re-order. Calls before the setter is wired fall back to UTC for
// period snapping (tested via the nil branch of tenantLocation).
func (s *Service) SetSettingsReader(r SettingsReader) {
	s.settings = r
}

// SetCustomerReader wires the customer fetcher used at sub-create time
// to inherit test_clock_id from the owning customer (ADR-027). When
// unwired (narrow unit tests), the new sub has no clock unless the
// caller stamps one directly. Production wires the real customer
// store via router.go.
func (s *Service) SetCustomerReader(r CustomerReader) {
	s.customers = r
}

// SetPlanReader wires the plan fetcher used to read BillingInterval
// at sub-lifecycle entry points (Create / Activate / EndTrial /
// ExtendTrial) for yearly-aware period anchoring. Optional —
// nil-safe; without it, the period helpers default to monthly math
// (pre-Bug-#10 behavior). Production wires the pricing store via
// router.go.
func (s *Service) SetPlanReader(r PlanReader) {
	s.plans = r
}

// SetEventDispatcher wires the outbound-webhook dispatcher used by
// the trial-expiry scan paths (ProcessExpiredTrialsForClock +
// ProcessExpiredTrials) to fire `subscription.trial_ended` events
// when status flips at trial_end_at. Engine auto-flip path emits
// the same event from billing/engine.go; this setter brings the
// catchup-orchestrator and wall-clock-cron paths into parity so
// webhook consumers see one event per trial transition regardless
// of which path activated the sub.
func (s *Service) SetEventDispatcher(d domain.EventDispatcher) {
	s.events = d
}

// SetBiller wires the billing engine for the in_advance first-invoice
// path (ADR-031). Optional — without it, in_advance plans silently
// behave like in_arrears until the next cycle close. Production wires
// *billing.Engine via router.go; unit tests can leave it unwired
// because most don't exercise the day-1 invoice path.
func (s *Service) SetBiller(b Biller) {
	s.biller = b
}

// SetResolver wires the unified clock.Resolver used to bind
// effective-now at operator entry points on clock-pinned entities.
// Once bound on ctx via clock.BindEffectiveNow, every downstream
// s.clock.Now(ctx) (including in the postgres store) returns
// frozen_time. Optional: nil leaves binding off and every callsite
// reads wall-clock — the test-friendly default.
//
// Replaces the per-service ClockResolver pattern shipped during the
// post-ADR-029 patches; now matches Stripe's model — entity pin is
// resolved once at the boundary, simulated time inherits down.
func (s *Service) SetResolver(r clock.Resolver) {
	s.resolver = r
}

// bindForCustomer binds effective-now from a customer pin. Used by
// Create, where the sub doesn't exist yet but the customer does.
// Failing on a dangling pin is worse than stamping wall-clock; on
// resolver error, returns ctx unchanged (downstream reads wall-clock).
func (s *Service) bindForCustomer(ctx context.Context, tenantID, customerID string) context.Context {
	bound, ok := clock.BindEffectiveNow(ctx, s.resolver, clock.Pin{TenantID: tenantID, CustomerID: customerID})
	if !ok && s.resolver != nil && customerID != "" {
		// Resolver wired but binding skipped — likely an error during
		// resolution. Log; don't fail the operator's action.
		slog.Warn("subscription: customer pin binding skipped, downstream uses wall-clock",
			"tenant_id", tenantID, "customer_id", customerID)
	}
	return bound
}

// bindForSub binds effective-now from a sub pin. Used by every
// per-sub mutation entry point (Activate, ChangeItem, ScheduleCancel,
// PauseCollection, EndTrial, ExtendTrial, Cancel).
func (s *Service) bindForSub(ctx context.Context, tenantID, subscriptionID string) context.Context {
	bound, ok := clock.BindEffectiveNow(ctx, s.resolver, clock.Pin{TenantID: tenantID, SubscriptionID: subscriptionID})
	if !ok && s.resolver != nil && subscriptionID != "" {
		slog.Warn("subscription: sub pin binding skipped, downstream uses wall-clock",
			"tenant_id", tenantID, "subscription_id", subscriptionID)
	}
	return bound
}

// tenantLocation resolves the tenant's preferred timezone (ADR-010).
// Errors and missing/invalid TZ strings collapse to UTC — the snap is
// a UX improvement over raw timestamps and shouldn't fail the create
// call when settings are unreadable. ADR-010-aligned with the
// dashboard's @/lib/dates helpers.
func (s *Service) tenantLocation(ctx context.Context, tenantID string) *time.Location {
	if s.settings == nil {
		return time.UTC
	}
	ts, err := s.settings.Get(ctx, tenantID)
	if err != nil || ts.Timezone == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(ts.Timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}

// beginningOfDayIn snaps `t` to 00:00:00 on its calendar date in `loc`,
// returned as a UTC instant for storage. Day-grade billing requires
// this to align UI-displayed dates with proration math (Chargebee /
// Lago / Recurly default).
func beginningOfDayIn(t time.Time, loc *time.Location) time.Time {
	local := t.In(loc)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc).UTC()
}

// beginningOfMonthIn snaps `t` to the first-of-month-at-00:00 in `loc`,
// returned as UTC. Calendar-billing anchor.
func beginningOfMonthIn(t time.Time, loc *time.Location) time.Time {
	local := t.In(loc)
	return time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, loc).UTC()
}

// firstPeriodAfterTrial computes (current_period_start, current_period_end)
// for the first post-trial billing period anchored on a trial_end_at instant.
//
// Yearly billing: always anniversary semantics (period = trial_end →
// trial_end + 1 year). billing_time is effectively ignored for yearly
// plans — Stripe ships no "calendar yearly" either, and there's no
// industry pattern for "stub to next Jan 1 then full year" billing.
//
// Monthly + calendar: start = trial_end (day-snapped), end = first day
// of the next calendar month. Produces a stub period (trial_end →
// next-month-start) that bills the partial month at the trial-end
// cycle close — Stripe / Lago parity. If trial_end IS already a
// calendar month boundary the stub collapses and the first period is
// a clean full cycle from that boundary.
//
// Monthly + anniversary: start = trial_end, end = trial_end + 1 month.
// No stub — cycle anchors on the trial-end instant.
//
// Subsequent cycles roll forward via billOnePeriod's per-cycle
// advanceBillingPeriod call (which is already interval-aware).
func firstPeriodAfterTrial(trialEnd time.Time, billingTime domain.SubscriptionBillingTime, interval domain.BillingInterval, loc *time.Location) (time.Time, time.Time) {
	ps := beginningOfDayIn(trialEnd, loc)
	if interval == domain.BillingYearly {
		return ps, ps.AddDate(1, 0, 0)
	}
	if billingTime == domain.BillingTimeCalendar {
		pe := beginningOfMonthIn(trialEnd.AddDate(0, 1, 0), loc)
		// Edge: trial_end fell exactly on a calendar boundary — the stub
		// computation collapses (ps == pe). Promote to a clean full cycle
		// from that boundary so the engine doesn't see a zero-length period.
		if !ps.Before(pe) {
			pe = beginningOfMonthIn(ps.AddDate(0, 1, 0), loc)
		}
		return ps, pe
	}
	return ps, ps.AddDate(0, 1, 0)
}

// firstPeriodForActivate computes the first billing period when a sub is
// activated without a trial — either via StartNow on Create, or via the
// operator's Activate on a draft sub.
//
// Yearly billing: always anniversary semantics — period begins at `at`
// (day-snapped) and ends at `at + 1 year`. Stripe doesn't ship calendar
// yearly either; a "stub to next Jan 1" model has no industry analog.
//
// Monthly + calendar: stub from `at` to the next calendar-month
// boundary (proration handled by billOnePeriod).
//
// Monthly + anniversary: full month from `at`.
func firstPeriodForActivate(at time.Time, billingTime domain.SubscriptionBillingTime, interval domain.BillingInterval, loc *time.Location) (time.Time, time.Time) {
	ps := beginningOfDayIn(at, loc)
	if interval == domain.BillingYearly {
		return ps, ps.AddDate(1, 0, 0)
	}
	if billingTime == domain.BillingTimeCalendar {
		return ps, beginningOfMonthIn(at.AddDate(0, 1, 0), loc)
	}
	return ps, ps.AddDate(0, 1, 0)
}

// rejectMixedItemIntervals validates that every item's plan shares
// the same BillingInterval AND the same BaseBillTiming. Returns
// errs.Invalid("items", ...) on mismatch.
//
//   - Interval mix: Stripe / Lago / Chargebee all reject mixed
//     intervals on a single sub because the period anchor is per-sub
//     and a monthly + yearly mix has no coherent cycle.
//   - Bill-timing mix: in_arrears and in_advance carry different
//     invoice-shape semantics (close elapsed vs open upcoming) and
//     mixing them on the same sub would emit inconsistent lines on the
//     same invoice. Velox's hybrid invoice shape assumes a uniform
//     bill_timing across items.
//
// Empty BillingInterval defaults to monthly; empty BaseBillTiming
// defaults to in_arrears — matching the engine's lenient defaults so
// pre-ADR-031 plans validate cleanly.
//
// Plan-fetch errors (RLS gap / deleted plan) surface as
// errs.Invalid so the operator gets a clean 400 instead of a 500.
func (s *Service) rejectMixedItemIntervals(ctx context.Context, tenantID string, items []domain.SubscriptionItem) error {
	if len(items) < 2 {
		return nil
	}
	first, err := s.plans.GetPlan(ctx, tenantID, items[0].PlanID)
	if err != nil {
		return errs.Invalid("items", fmt.Sprintf("plan %q not found", items[0].PlanID))
	}
	firstInterval := first.BillingInterval
	if firstInterval == "" {
		firstInterval = domain.BillingMonthly
	}
	firstTiming := first.BaseBillTiming
	if firstTiming == "" {
		firstTiming = domain.BillInArrears
	}
	for _, it := range items[1:] {
		plan, err := s.plans.GetPlan(ctx, tenantID, it.PlanID)
		if err != nil {
			return errs.Invalid("items", fmt.Sprintf("plan %q not found", it.PlanID))
		}
		otherInterval := plan.BillingInterval
		if otherInterval == "" {
			otherInterval = domain.BillingMonthly
		}
		if otherInterval != firstInterval {
			return errs.Invalid("items", fmt.Sprintf(
				"all items must share the same billing interval (plan %q is %s, plan %q is %s)",
				items[0].PlanID, firstInterval, it.PlanID, otherInterval))
		}
		otherTiming := plan.BaseBillTiming
		if otherTiming == "" {
			otherTiming = domain.BillInArrears
		}
		if otherTiming != firstTiming {
			return errs.Invalid("items", fmt.Sprintf(
				"all items must share the same bill_timing (plan %q is %s, plan %q is %s)",
				items[0].PlanID, firstTiming, it.PlanID, otherTiming))
		}
	}
	return nil
}

// firstPlanInterval returns the BillingInterval to use for period
// anchoring on a sub. Reads the first item's plan via PlanReader; on
// any failure (reader not wired, plan deleted, RLS gap) defaults to
// BillingMonthly — the pre-Bug-#10 behavior, so unwired test paths
// don't break.
//
// Stripe / Lago reject mixed intervals on the same sub. Velox should
// too eventually; for now the first-item-wins approach is consistent
// with how plans / currencies are resolved elsewhere (engine.go's
// firstPlanCurrency pattern).
func (s *Service) firstPlanInterval(ctx context.Context, tenantID string, items []domain.SubscriptionItem) domain.BillingInterval {
	if s.plans == nil || len(items) == 0 {
		return domain.BillingMonthly
	}
	plan, err := s.plans.GetPlan(ctx, tenantID, items[0].PlanID)
	if err != nil || plan.BillingInterval == "" {
		return domain.BillingMonthly
	}
	return plan.BillingInterval
}

// CreateItemInput is a single priced line the caller wants on a new
// subscription. At least one item is required; duplicate plan_ids are rejected
// so the underlying UNIQUE (subscription_id, plan_id) never surfaces as a
// mid-transaction conflict.
type CreateItemInput struct {
	PlanID   string `json:"plan_id"`
	Quantity int64  `json:"quantity,omitempty"`
}

type CreateInput struct {
	Code          string                         `json:"code"`
	DisplayName   string                         `json:"display_name"`
	CustomerID    string                         `json:"customer_id"`
	Items         []CreateItemInput              `json:"items"`
	BillingTime   domain.SubscriptionBillingTime `json:"billing_time"`
	TrialDays     int                            `json:"trial_days,omitempty"`
	StartNow      bool                           `json:"start_now,omitempty"`
	UsageCapUnits *int64                         `json:"usage_cap_units,omitempty"`
	OverageAction string                         `json:"overage_action,omitempty"`
}

func (s *Service) Create(ctx context.Context, tenantID string, input CreateInput) (domain.Subscription, error) {
	code := strings.TrimSpace(input.Code)
	displayName := strings.TrimSpace(input.DisplayName)

	if code == "" {
		return domain.Subscription{}, errs.Required("code")
	}
	if !slugPattern.MatchString(code) {
		return domain.Subscription{}, errs.Invalid("code", "must contain only alphanumeric characters, hyphens, and underscores")
	}
	if displayName == "" {
		return domain.Subscription{}, errs.Required("display_name")
	}
	if input.CustomerID == "" {
		return domain.Subscription{}, errs.Required("customer_id")
	}
	if len(input.Items) == 0 {
		return domain.Subscription{}, errs.Required("items")
	}

	seen := make(map[string]struct{}, len(input.Items))
	items := make([]domain.SubscriptionItem, 0, len(input.Items))
	for i, in := range input.Items {
		if in.PlanID == "" {
			return domain.Subscription{}, errs.Required(fmt.Sprintf("items[%d].plan_id", i))
		}
		if _, dup := seen[in.PlanID]; dup {
			return domain.Subscription{}, errs.Invalid("items", fmt.Sprintf("duplicate plan_id %q", in.PlanID))
		}
		seen[in.PlanID] = struct{}{}
		qty := in.Quantity
		if qty == 0 {
			qty = 1
		}
		if qty < 1 {
			return domain.Subscription{}, errs.Invalid(fmt.Sprintf("items[%d].quantity", i), "must be >= 1")
		}
		items = append(items, domain.SubscriptionItem{
			PlanID:   in.PlanID,
			Quantity: qty,
		})
	}

	billingTime := input.BillingTime
	if billingTime == "" {
		billingTime = domain.BillingTimeCalendar
	}
	if billingTime != domain.BillingTimeCalendar && billingTime != domain.BillingTimeAnniversary {
		return domain.Subscription{}, errs.Invalid("billing_time", "must be calendar or anniversary")
	}

	// Reject mixed billing intervals across items (Stripe / Lago parity).
	// All items on a sub must share an interval — the period anchor is
	// per-sub, so a monthly + yearly mix has no coherent cycle. Skipped
	// when PlanReader isn't wired (narrow unit tests) or when there's
	// only one item.
	if s.plans != nil && len(items) > 1 {
		if err := s.rejectMixedItemIntervals(ctx, tenantID, items); err != nil {
			return domain.Subscription{}, err
		}
	}

	// Customer-level test-clock attach (ADR-027, Stripe parity): a
	// sub's clock is unconditionally inherited from its owning customer.
	// The API does not accept a per-sub test_clock_id — Stripe doesn't
	// either, and accepting one only created a redundant validation
	// path against the canonical customer-level value.
	//
	// Skipped when the customer reader isn't wired (narrow unit-test
	// path); the sub then has no clock unless the test sets one
	// directly on the domain.Subscription it constructs.
	var inheritedClockID string
	if s.customers != nil {
		if cust, err := s.customers.Get(ctx, tenantID, input.CustomerID); err == nil {
			inheritedClockID = cust.TestClockID
		}
		// If err != nil here, fall through — the downstream FK check
		// on customer_id will fail with a clean 400.
	}

	status := domain.SubscriptionDraft
	// Bind effective-now from the customer's test_clock pin (if any).
	// Every downstream s.clock.Now(ctx) — including in the postgres
	// store's created_at / updated_at writes — inherits the simulated
	// time. Mirrors Stripe's "no semantic change" guarantee at
	// resource-create.
	ctx = s.bindForCustomer(ctx, tenantID, input.CustomerID)
	now := s.clock.Now(ctx)

	var trialStart, trialEnd *time.Time
	var startedAt *time.Time

	var periodStart, periodEnd, nextBilling *time.Time

	// Resolve tenant TZ once for all period-boundary snaps below. Day-
	// grade billing (Chargebee / Lago default) snaps both endpoints to
	// 00:00 in tenant TZ so the "Period: <start> - <end>" UI exactly
	// matches the proration math. Without this, a sub created at 14:00
	// on the 1st gets billed for 30/31 days even though the UI shows
	// May 1 → Jun 1 — see service_test.go TestPeriod_DayGradeSnap.
	loc := s.tenantLocation(ctx, tenantID)

	if input.TrialDays > 0 {
		ts := now
		te := now.AddDate(0, 0, input.TrialDays)
		trialStart = &ts
		trialEnd = &te
		status = domain.SubscriptionTrialing
		startedAt = &now
		// First post-trial period anchors on trial_end. Yearly plans
		// get a full year from trial-end; monthly + calendar produces
		// the trial_end → next-month-start stub (Stripe parity);
		// monthly + anniversary produces a full month from trial_end.
		// See helper doc for edge cases.
		interval := s.firstPlanInterval(ctx, tenantID, items)
		ps, pe := firstPeriodAfterTrial(te, billingTime, interval, loc)
		periodStart = &ps
		periodEnd = &pe
		nextBilling = &pe
	} else if input.StartNow {
		status = domain.SubscriptionActive
		startedAt = &now
		interval := s.firstPlanInterval(ctx, tenantID, items)
		ps, pe := firstPeriodForActivate(now, billingTime, interval, loc)
		periodStart = &ps
		periodEnd = &pe
		nextBilling = &pe
	}

	overageAction := input.OverageAction
	if overageAction == "" {
		overageAction = "charge"
	}

	sub, err := s.store.Create(ctx, tenantID, domain.Subscription{
		Code:                      code,
		DisplayName:               displayName,
		CustomerID:                input.CustomerID,
		Status:                    status,
		BillingTime:               billingTime,
		TrialStartAt:              trialStart,
		TrialEndAt:                trialEnd,
		StartedAt:                 startedAt,
		CurrentBillingPeriodStart: periodStart,
		CurrentBillingPeriodEnd:   periodEnd,
		NextBillingAt:             nextBilling,
		UsageCapUnits:             input.UsageCapUnits,
		OverageAction:             overageAction,
		TestClockID:               inheritedClockID,
		Items:                     items,
	})
	if err != nil {
		return domain.Subscription{}, err
	}

	// ADR-031: in_advance plans get a day-1 invoice covering the
	// upcoming period's base fee. Best-effort — a failure here logs
	// but doesn't roll back the sub. Trialing subs skip this path
	// (their first invoice fires when the trial ends, via the
	// cycle scheduler picking up the now-active sub at trial_end_at).
	if s.biller != nil && sub.Status == domain.SubscriptionActive {
		if _, err := s.biller.BillOnCreate(ctx, sub); err != nil {
			slog.Warn("first-invoice-on-create failed; in_advance base fee will be deferred to next cycle close",
				"subscription_id", sub.ID,
				"tenant_id", sub.TenantID,
				"error", err)
		}
	}

	return sub, nil
}

func (s *Service) Get(ctx context.Context, tenantID, id string) (domain.Subscription, error) {
	return s.store.Get(ctx, tenantID, id)
}

func (s *Service) List(ctx context.Context, filter ListFilter) ([]domain.Subscription, int, error) {
	return s.store.List(ctx, filter)
}

func (s *Service) Activate(ctx context.Context, tenantID, id string) (domain.Subscription, error) {
	sub, err := s.store.Get(ctx, tenantID, id)
	if err != nil {
		return domain.Subscription{}, err
	}
	if sub.Status != domain.SubscriptionDraft {
		return domain.Subscription{}, errs.InvalidState(fmt.Sprintf("can only activate draft subscriptions, current status: %s", sub.Status))
	}

	// Bind effective-now via the sub's TestClockID; downstream stamps
	// inherit. Activate's writes (activated_at, started_at, period
	// bounds, next_billing_at, updated_at on the sub row) all land in
	// simulated time on clock-pinned subs.
	ctx = s.bindForSub(ctx, tenantID, id)
	now := s.clock.Now(ctx)
	sub.Status = domain.SubscriptionActive
	sub.ActivatedAt = &now
	sub.StartedAt = &now

	if sub.CurrentBillingPeriodStart == nil {
		// Activate uses the same first-period helper as Create's
		// StartNow branch: period begins at `now` (day-snapped) and
		// ends at the next calendar boundary (calendar billing) or
		// `now + 1 month` (anniversary). Pre-fix this hardcoded
		// beginningOfMonth(now) which BACKDATED periodStart to the
		// first of the current month — a sub activated Nov 29 was
		// billed for the full Nov cycle including the days it was a
		// draft. Pre-fix also ignored sub.BillingTime entirely — an
		// anniversary draft activated mid-month still got calendar-
		// anchored periods.
		loc := s.tenantLocation(ctx, tenantID)
		interval := s.firstPlanInterval(ctx, tenantID, sub.Items)
		ps, pe := firstPeriodForActivate(now, sub.BillingTime, interval, loc)
		sub.CurrentBillingPeriodStart = &ps
		sub.CurrentBillingPeriodEnd = &pe
		sub.NextBillingAt = &pe
	}

	return s.store.Update(ctx, tenantID, sub)
}

// ---- Items ----

// AddItemInput adds a new priced line to an existing subscription.
type AddItemInput struct {
	PlanID   string `json:"plan_id"`
	Quantity int64  `json:"quantity,omitempty"`
}

// UpdateItemInput mutates a single item. Exactly one of {Quantity, NewPlanID}
// may be supplied per call — separating the two keeps the proration branches
// distinct and avoids having to reason about "changed plan and quantity in
// one shot" edge cases. Quantity changes settle within the current billing
// period via the quantity-proration code path (separate from plan-change
// proration). Plan changes follow Immediate/scheduled semantics mirroring the
// prior ChangePlan behaviour, now per-item.
type UpdateItemInput struct {
	Quantity  *int64 `json:"quantity,omitempty"`
	NewPlanID string `json:"new_plan_id,omitempty"`
	Immediate bool   `json:"immediate,omitempty"`
}

// ItemChangeResult mirrors ChangePlanResult but scoped to a single item. The
// Proration payload is stamped by the billing layer when the caller requests
// an immediate plan change; AddItem/RemoveItem/quantity-only edits return
// nil Proration (their proration goes through separate invoice/credit lines
// stitched in at next-cycle close).
type ItemChangeResult struct {
	Item        domain.SubscriptionItem `json:"item"`
	EffectiveAt time.Time               `json:"effective_at"`
	Proration   *ProrationDetail        `json:"proration,omitempty"`
}

type ProrationDetail struct {
	OldPlanID       string  `json:"old_plan_id"`
	NewPlanID       string  `json:"new_plan_id"`
	ProrationFactor float64 `json:"proration_factor"`
	AmountCents     int64   `json:"amount_cents"`
	Type            string  `json:"type"` // "invoice" or "credit"
	InvoiceID       string  `json:"invoice_id,omitempty"`
}

func (s *Service) AddItem(ctx context.Context, tenantID, subscriptionID string, input AddItemInput) (domain.SubscriptionItem, error) {
	if input.PlanID == "" {
		return domain.SubscriptionItem{}, errs.Required("plan_id")
	}
	qty := input.Quantity
	if qty == 0 {
		qty = 1
	}
	if qty < 1 {
		return domain.SubscriptionItem{}, errs.Invalid("quantity", "must be >= 1")
	}

	sub, err := s.store.Get(ctx, tenantID, subscriptionID)
	if err != nil {
		return domain.SubscriptionItem{}, err
	}
	if sub.Status == domain.SubscriptionCanceled || sub.Status == domain.SubscriptionArchived {
		return domain.SubscriptionItem{}, errs.InvalidState(fmt.Sprintf("cannot add items to %s subscriptions", sub.Status))
	}

	// Mixed-interval guard: new item's plan must match the sub's
	// existing item intervals. Same rationale as Create — the period
	// anchor is per-sub, so adding a yearly item to a monthly sub
	// would have no coherent cycle. Skipped when PlanReader isn't
	// wired (narrow unit tests).
	if s.plans != nil && len(sub.Items) > 0 {
		newPlusExisting := append([]domain.SubscriptionItem{{PlanID: input.PlanID}}, sub.Items...)
		if err := s.rejectMixedItemIntervals(ctx, tenantID, newPlusExisting); err != nil {
			return domain.SubscriptionItem{}, err
		}
	}

	ctx = s.bindForSub(ctx, tenantID, subscriptionID)
	return s.store.AddItem(ctx, tenantID, domain.SubscriptionItem{
		SubscriptionID: subscriptionID,
		PlanID:         input.PlanID,
		Quantity:       qty,
	})
}

// UpdateItem applies a quantity-only patch OR a plan change (immediate or
// scheduled) to a single item. Exactly one of Quantity/NewPlanID must be set.
// Plan change semantics match the prior subscription-level ChangePlan: an
// immediate change supersedes any existing pending change on the same item,
// while a scheduled change records pending_plan_id + effective_at for the
// billing engine to apply at the next cycle boundary.
func (s *Service) UpdateItem(ctx context.Context, tenantID, subscriptionID, itemID string, input UpdateItemInput) (ItemChangeResult, error) {
	if input.Quantity == nil && input.NewPlanID == "" {
		return ItemChangeResult{}, errs.Invalid("body", "one of quantity or new_plan_id is required")
	}
	if input.Quantity != nil && input.NewPlanID != "" {
		return ItemChangeResult{}, errs.Invalid("body", "quantity and new_plan_id cannot be set together; issue two requests")
	}

	item, err := s.store.GetItem(ctx, tenantID, itemID)
	if err != nil {
		return ItemChangeResult{}, err
	}
	if item.SubscriptionID != subscriptionID {
		// Scoping the item to its parent keeps a tenant from mutating an item
		// on a subscription they didn't supply in the URL — the tenant_id RLS
		// check already blocks cross-tenant, but intra-tenant cross-sub has to
		// be enforced here.
		return ItemChangeResult{}, errs.ErrNotFound
	}
	sub, err := s.store.Get(ctx, tenantID, subscriptionID)
	if err != nil {
		return ItemChangeResult{}, err
	}
	if sub.Status != domain.SubscriptionActive {
		return ItemChangeResult{}, errs.InvalidState(fmt.Sprintf("can only modify items on active subscriptions, current status: %s", sub.Status))
	}

	// Bind via sub pin: pending_plan_effective_at (and the immediate-
	// effectiveAt return) need to be in simulated time so the engine's
	// catchup picks up the rollover at the operator's next Advance,
	// not at wall-clock-2026.
	ctx = s.bindForSub(ctx, tenantID, subscriptionID)
	now := s.clock.Now(ctx)

	if input.Quantity != nil {
		if *input.Quantity < 1 {
			return ItemChangeResult{}, errs.Invalid("quantity", "must be >= 1")
		}
		if *input.Quantity == item.Quantity {
			return ItemChangeResult{}, errs.Invalid("quantity", "new quantity is the same as current quantity")
		}
		updated, err := s.store.UpdateItemQuantity(ctx, tenantID, itemID, *input.Quantity)
		if err != nil {
			return ItemChangeResult{}, err
		}
		return ItemChangeResult{Item: updated, EffectiveAt: now}, nil
	}

	if input.NewPlanID == item.PlanID {
		return ItemChangeResult{}, errs.Invalid("new_plan_id", "new plan is the same as current plan")
	}

	// Mixed-interval / mixed-bill-timing guard on plan-change. Composes
	// the post-change item set (other items unchanged + the new plan on
	// this item) and asserts every interval AND every bill_timing
	// matches. Same rationale as Create / AddItem — the period anchor
	// and invoice shape are per-sub and would drift if a single item
	// swapped to a different cadence. Skipped when PlanReader is
	// unwired (narrow unit tests).
	//
	// The multi-item check catches mixes; the inline single-item guard
	// below catches the case where swapping the only item changes the
	// sub's bill_timing (e.g. in_arrears $29 → in_advance $49). The
	// engine's hybrid-invoice shape assumes a uniform bill_timing per
	// sub; cross-bill-timing swaps are not exercised end-to-end, so
	// rejecting at request time is the pre-launch safe stance.
	if s.plans != nil {
		hypothetical := make([]domain.SubscriptionItem, 0, len(sub.Items))
		for _, existing := range sub.Items {
			if existing.ID == itemID {
				hypothetical = append(hypothetical, domain.SubscriptionItem{PlanID: input.NewPlanID})
			} else {
				hypothetical = append(hypothetical, existing)
			}
		}
		if err := s.rejectMixedItemIntervals(ctx, tenantID, hypothetical); err != nil {
			return ItemChangeResult{}, err
		}

		// Single-item bill_timing-change guard. The multi-item check
		// above can't fire when len(items)==1, so explicitly compare
		// the current item's plan timing against the new plan's
		// timing. Both legs default to in_arrears for pre-ADR-031
		// plans to match the engine's lenient defaults.
		currentPlan, err := s.plans.GetPlan(ctx, tenantID, item.PlanID)
		if err != nil {
			return ItemChangeResult{}, errs.Invalid("items", fmt.Sprintf("plan %q not found", item.PlanID))
		}
		newPlan, err := s.plans.GetPlan(ctx, tenantID, input.NewPlanID)
		if err != nil {
			return ItemChangeResult{}, errs.Invalid("new_plan_id", fmt.Sprintf("plan %q not found", input.NewPlanID))
		}
		currentTiming := currentPlan.BaseBillTiming
		if currentTiming == "" {
			currentTiming = domain.BillInArrears
		}
		newTiming := newPlan.BaseBillTiming
		if newTiming == "" {
			newTiming = domain.BillInArrears
		}
		if currentTiming != newTiming {
			return ItemChangeResult{}, errs.Invalid("new_plan_id", fmt.Sprintf(
				"bill_timing change is not supported on plan-swap (current %s, new %s); cancel the subscription and start a new one with the target plan",
				currentTiming, newTiming))
		}

		// Cross-interval immediate-swap guard. The proration math
		// (`(newAmount-oldAmount) * remainingPeriodFactor`) only
		// produces a coherent number when both plans share an
		// interval — comparing a monthly $29 to a yearly $588 inside
		// a "remaining-month proportion" factor charges the customer
		// 2/3 of the YEARLY delta for the rest of a month. Industry-
		// standard: Stripe requires aligned schedules; Lago / Orb
		// don't allow immediate cross-interval. Scheduled (immediate=
		// false) is fine — the swap fires at the boundary, the
		// engine bills the closing period under the outgoing plan,
		// and the new yearly cycle starts clean.
		currentInterval := currentPlan.BillingInterval
		if currentInterval == "" {
			currentInterval = domain.BillingMonthly
		}
		newInterval := newPlan.BillingInterval
		if newInterval == "" {
			newInterval = domain.BillingMonthly
		}
		if input.Immediate && currentInterval != newInterval {
			return ItemChangeResult{}, errs.Invalid("immediate", fmt.Sprintf(
				"immediate plan-swap across billing intervals is not supported (current %s, new %s); set immediate=false to schedule the swap at the next period boundary, where the closing invoice bills under the outgoing plan and the new interval cycle starts cleanly",
				currentInterval, newInterval))
		}
	}

	if !input.Immediate {
		var effectiveAt time.Time
		if sub.CurrentBillingPeriodEnd != nil {
			effectiveAt = *sub.CurrentBillingPeriodEnd
		} else {
			effectiveAt = now
		}
		updated, err := s.store.SetItemPendingPlan(ctx, tenantID, itemID, input.NewPlanID, effectiveAt)
		if err != nil {
			return ItemChangeResult{}, err
		}
		return ItemChangeResult{Item: updated, EffectiveAt: effectiveAt}, nil
	}

	updated, err := s.store.ApplyItemPlanImmediately(ctx, tenantID, itemID, input.NewPlanID, now)
	if err != nil {
		return ItemChangeResult{}, err
	}
	return ItemChangeResult{Item: updated, EffectiveAt: now}, nil
}

// CancelPendingItemChange clears a scheduled plan change on a single item.
// Idempotent — a no-op if nothing was scheduled.
func (s *Service) CancelPendingItemChange(ctx context.Context, tenantID, subscriptionID, itemID string) (domain.SubscriptionItem, error) {
	item, err := s.store.GetItem(ctx, tenantID, itemID)
	if err != nil {
		return domain.SubscriptionItem{}, err
	}
	if item.SubscriptionID != subscriptionID {
		return domain.SubscriptionItem{}, errs.ErrNotFound
	}
	if item.PendingPlanID == "" {
		return item, nil
	}
	ctx = s.bindForSub(ctx, tenantID, subscriptionID)
	return s.store.ClearItemPendingPlan(ctx, tenantID, itemID)
}

// RemoveItem deletes an item. Removing the only remaining item on an active
// subscription is rejected — a subscription with zero priced lines has no
// valid billing semantics. Callers wanting to end billing altogether should
// Cancel the subscription.
func (s *Service) RemoveItem(ctx context.Context, tenantID, subscriptionID, itemID string) error {
	item, err := s.store.GetItem(ctx, tenantID, itemID)
	if err != nil {
		return err
	}
	if item.SubscriptionID != subscriptionID {
		return errs.ErrNotFound
	}
	sub, err := s.store.Get(ctx, tenantID, subscriptionID)
	if err != nil {
		return err
	}
	if sub.Status == domain.SubscriptionActive && len(sub.Items) <= 1 {
		return errs.InvalidState("cannot remove the last item from an active subscription; cancel the subscription instead")
	}
	ctx = s.bindForSub(ctx, tenantID, subscriptionID)
	return s.store.RemoveItem(ctx, tenantID, itemID)
}

// Cancel returns the canceled subscription, the cents-amount of any
// cancel-proration credit granted (0 when none — in_arrears sub,
// clean cancel, unpaid source invoice), and an error. The handler
// surfaces the credit amount in the audit-log entry so the activity
// timeline shows "Subscription canceled · Prorated credit $X.XX"
// (industry standard — Stripe / Lago / Chargebee / Orb all surface
// the credit on the subscription timeline, not just the customer's
// credit balance).
func (s *Service) Cancel(ctx context.Context, tenantID, id string) (domain.Subscription, int64, error) {
	ctx = s.bindForSub(ctx, tenantID, id)
	canceled, err := s.store.CancelAtomic(ctx, tenantID, id)
	if err != nil {
		return domain.Subscription{}, 0, err
	}

	// PR-10: emit the final partial-period invoice for any mid-period
	// cancel. Covers in_arrears prorated base + usage from
	// current_period_start → canceled_at. No-op when canceled_at lands
	// at/after current_period_end (clean cancel — cycle close handles
	// it normally) or before current_period_start (defensive).
	// Best-effort; operator can manually invoice from the dashboard
	// if this fails. Runs BEFORE BillOnCancel so the credit grant
	// (in_advance unused-base refund) doesn't pre-apply against this
	// invoice — credit application is a separate balance operation,
	// independent of the final-on-cancel invoice line items.
	if s.biller != nil {
		if _, err := s.biller.BillFinalOnImmediateCancel(ctx, canceled); err != nil {
			slog.Warn("final-on-cancel invoice failed; partial-period usage may be uninvoiced — operator can issue manually",
				"subscription_id", canceled.ID,
				"tenant_id", tenantID,
				"error", err)
		}
	}

	// ADR-031: in_advance plans get a cancel-proration credit for the
	// unused portion of an already-billed period. Best-effort — logs
	// on failure; operator can manually issue a credit grant from
	// the dashboard if needed. No-op for in_arrears plans. Returns
	// the cents amount granted so the handler can stamp it onto the
	// cancel audit-log entry (powers the timeline "Prorated credit
	// $X.XX" detail line).
	var prorationCreditCents int64
	if s.biller != nil {
		amt, err := s.biller.BillOnCancel(ctx, canceled)
		if err != nil {
			slog.Warn("cancel proration failed; manual credit may be required",
				"subscription_id", canceled.ID,
				"tenant_id", tenantID,
				"error", err)
		} else {
			prorationCreditCents = amt
		}
	}

	return canceled, prorationCreditCents, nil
}

// ScheduleCancelInput carries the soft-cancel intent. Exactly one of
// AtPeriodEnd or CancelAt must be set on a single call. AtPeriodEnd defers
// the cancel to current_billing_period_end; CancelAt is an explicit
// timestamp the cycle scan compares against effectiveNow. The mutually-
// exclusive split forces unambiguous caller intent — Stripe's update
// endpoint accepts both fields together but the resulting precedence is
// surprising; rejecting the combination here keeps the API obvious.
type ScheduleCancelInput struct {
	AtPeriodEnd bool       `json:"at_period_end,omitempty"`
	CancelAt    *time.Time `json:"cancel_at,omitempty"`
}

// ScheduleCancel persists the soft-cancel intent. v1 only accepts
// CancelAt values >= current_billing_period_end so the active period
// bills normally and the cancel lands on a clean cycle boundary; the
// shorten-current-period + proration variant is a follow-up that needs
// the proration generator wired into the engine cancel path.
//
// Re-scheduling is idempotent: a second call with the same intent leaves
// the row unchanged but for updated_at. Toggling between modes (e.g.
// AtPeriodEnd → CancelAt) is allowed because each call is a full
// replacement of the row's schedule fields.
func (s *Service) ScheduleCancel(ctx context.Context, tenantID, id string, input ScheduleCancelInput) (domain.Subscription, error) {
	if !input.AtPeriodEnd && input.CancelAt == nil {
		return domain.Subscription{}, errs.Invalid("body", "one of at_period_end or cancel_at must be set")
	}
	if input.AtPeriodEnd && input.CancelAt != nil {
		return domain.Subscription{}, errs.Invalid("body", "at_period_end and cancel_at cannot be set together; pick one")
	}

	sub, err := s.store.Get(ctx, tenantID, id)
	if err != nil {
		return domain.Subscription{}, err
	}

	// Future-check the operator-supplied cancel_at against simulated
	// time on clock-pinned subs. Without this, an operator who set
	// cancel_at = "1 month from frozen_now" would be rejected as past
	// because wall-clock has drifted ahead of frozen_time.
	ctx = s.bindForSub(ctx, tenantID, id)
	now := s.clock.Now(ctx)
	var cancelAt *time.Time
	if input.CancelAt != nil {
		ts := input.CancelAt.UTC()
		if !ts.After(now) {
			return domain.Subscription{}, errs.Invalid("cancel_at", "must be in the future")
		}
		// v1 constraint — see function comment.
		if sub.CurrentBillingPeriodEnd != nil && ts.Before(*sub.CurrentBillingPeriodEnd) {
			return domain.Subscription{}, errs.Invalid("cancel_at",
				"must be on or after current_billing_period_end (mid-period cancel with proration is not yet supported)")
		}
		cancelAt = &ts
	}

	return s.store.ScheduleCancellation(ctx, tenantID, id, cancelAt, input.AtPeriodEnd)
}

// ClearScheduledCancel undoes any prior schedule. Idempotent — a row
// without a schedule returns unchanged.
func (s *Service) ClearScheduledCancel(ctx context.Context, tenantID, id string) (domain.Subscription, error) {
	ctx = s.bindForSub(ctx, tenantID, id)
	return s.store.ClearScheduledCancellation(ctx, tenantID, id)
}

// PauseCollectionInput carries the collection-pause intent. Behavior is
// required and must be one of the supported modes (v1: keep_as_draft).
// ResumesAt is optional; when set, the cycle scan auto-clears the pause
// at the start of the period containing or after that timestamp so that
// period bills normally. When nil, only an explicit DELETE clears it.
type PauseCollectionInput struct {
	Behavior  domain.PauseCollectionBehavior `json:"behavior"`
	ResumesAt *time.Time                     `json:"resumes_at,omitempty"`
}

// PauseCollection sets the Stripe-parity collection-pause state. Distinct
// from Pause (hard freeze on status). The cycle keeps advancing; the engine
// generates the invoice as draft and skips finalize/charge/dunning while
// pause_collection is non-null.
//
// Idempotent: a second call with the same input replaces the row's
// pause_collection_* columns with the same values. Switching from one
// resumes_at to another is supported because each call is a full
// replacement.
func (s *Service) PauseCollection(ctx context.Context, tenantID, id string, input PauseCollectionInput) (domain.Subscription, error) {
	if input.Behavior == "" {
		return domain.Subscription{}, errs.Invalid("behavior", "behavior is required")
	}
	if input.Behavior != domain.PauseCollectionKeepAsDraft {
		return domain.Subscription{}, errs.Invalid("behavior",
			"only 'keep_as_draft' is supported in v1; mark_uncollectible and void require an uncollectible invoice status that does not yet exist")
	}

	// Bind to the sub pin upfront so the store's sub.UpdatedAt stamp +
	// any downstream clock.Now reads honor simulated time on
	// clock-pinned subs. Pre-fix the bind was only inside the
	// `if input.ResumesAt != nil` branch, so the common no-resumes_at
	// path stamped wall-clock UpdatedAt — which then propagated through
	// auditCtxForSub to wall-clock audit rows.
	ctx = s.bindForSub(ctx, tenantID, id)

	pc := domain.PauseCollection{Behavior: input.Behavior}
	if input.ResumesAt != nil {
		ts := input.ResumesAt.UTC()
		if !ts.After(s.clock.Now(ctx)) {
			return domain.Subscription{}, errs.Invalid("resumes_at", "must be in the future")
		}
		pc.ResumesAt = &ts
	}

	return s.store.SetPauseCollection(ctx, tenantID, id, pc)
}

// ResumeCollection clears any active collection-pause. Idempotent — a row
// without an active pause returns unchanged. Distinct from Resume (which
// flips status from paused back to active).
func (s *Service) ResumeCollection(ctx context.Context, tenantID, id string) (domain.Subscription, error) {
	ctx = s.bindForSub(ctx, tenantID, id)
	return s.store.ClearPauseCollection(ctx, tenantID, id)
}

// EndTrial transitions a 'trialing' subscription to 'active' immediately,
// regardless of trial_end_at. Operator-driven counterpart to the cycle-
// scan auto-flip — used when the customer wants to start paying ahead of
// the trial schedule, or the operator is upgrading them off a free trial
// after a sales call. Idempotent at the SQL level (the store atomic
// returns errs.InvalidState if the row is already active or terminal).
func (s *Service) EndTrial(ctx context.Context, tenantID, id string) (domain.Subscription, error) {
	ctx = s.bindForSub(ctx, tenantID, id)
	now := s.clock.Now(ctx)

	// Read sub for billing_time + status validation. Early-end resets
	// the period anchor so billing starts immediately (Stripe parity);
	// the engine-auto-flip path (billOnePeriod calling ActivateAfterTrial
	// at cycle close) is the other transition path and leaves periods
	// alone because they were already advanced to the cycle boundary.
	sub, err := s.store.Get(ctx, tenantID, id)
	if err != nil {
		return domain.Subscription{}, err
	}
	if sub.Status != domain.SubscriptionTrialing {
		return domain.Subscription{}, errs.InvalidState(fmt.Sprintf("cannot end trial on %s subscription", sub.Status))
	}

	loc := s.tenantLocation(ctx, tenantID)
	interval := s.firstPlanInterval(ctx, tenantID, sub.Items)
	ps, pe := firstPeriodForActivate(now, sub.BillingTime, interval, loc)
	activated, err := s.store.EndTrialEarly(ctx, tenantID, id, now, ps, pe, pe)
	if err != nil {
		return domain.Subscription{}, err
	}

	// ADR-031: in_advance items get a day-1 invoice covering the first
	// paid period (now → period_end) at the activation instant. Mirrors
	// Service.Create's same call for non-trial subs. Best-effort —
	// failures here log but don't roll back the activation; operator
	// can manually issue the invoice from the dashboard. No-op when
	// every item is in_arrears (those wait for cycle close).
	//
	// Idempotent via the (sub_id, period_start, period_end) UNIQUE
	// constraint — a retry doesn't double-bill.
	if s.biller != nil {
		if _, err := s.biller.BillOnCreate(ctx, activated); err != nil {
			slog.Warn("first-invoice-on-trial-end failed; in_advance base fee will be deferred to next cycle close",
				"subscription_id", activated.ID,
				"tenant_id", tenantID,
				"error", err)
		}
	}

	return activated, nil
}

// ProcessExpiredTrialsForClock is the catchup Phase 0.5 entry point —
// scans subs pinned to this clock whose trial has elapsed in sim time
// and flips each from 'trialing' to 'active' at trial_end_at (not at
// the later cycle close). Bug #8: without this, status stays
// 'trialing' for the gap between trial_end_at and the first
// chargeable cycle close (up to ~30 days for calendar billing).
//
// Per sub: ActivateAfterTrial(at=trial_end_at) for the atomic status
// flip + activated_at stamp, then BillOnCreate to cover in_advance
// items' first paid period at trial_end_at (Bug #6 coverage carries
// through). Failures collected but don't abort the batch — one bad
// sub doesn't stall the others. Returns (processed_count, errors).
//
// Subscriptions matched by the scan but already-EndTrial'd by an
// operator race will land in ActivateAfterTrial's InvalidState
// branch — treated as a no-op (already correct state).
func (s *Service) ProcessExpiredTrialsForClock(ctx context.Context, tenantID, clockID string, frozen time.Time) (int, []error) {
	expired, err := s.store.ListExpiredTrialsForClock(ctx, tenantID, clockID, frozen, 100)
	if err != nil {
		return 0, []error{fmt.Errorf("list expired trials: %w", err)}
	}

	var (
		processed int
		batchErrs []error
	)
	for _, sub := range expired {
		if sub.TrialEndAt == nil {
			continue
		}
		trialEndAt := *sub.TrialEndAt
		bound := s.bindForSub(ctx, tenantID, sub.ID)
		activated, err := s.store.ActivateAfterTrial(bound, tenantID, sub.ID, trialEndAt)
		if err != nil {
			// Operator-EndTrial race: the row already left 'trialing'
			// between the scan SELECT and the UPDATE. Not an error —
			// just a no-op for this pass.
			if errors.Is(err, errs.ErrInvalidState) {
				continue
			}
			batchErrs = append(batchErrs, fmt.Errorf("activate sub %s: %w", sub.ID, err))
			continue
		}

		// Cover the first paid period for in_advance items at the
		// activation instant (Bug #6 carry-through). No-op when no
		// item is in_advance. Idempotent via the invoice UNIQUE
		// constraint — a re-run on the same sub doesn't double-bill.
		// Failure logs but doesn't roll back the activation — same
		// shape as Service.EndTrial.
		if s.biller != nil {
			if _, err := s.biller.BillOnCreate(bound, activated); err != nil {
				slog.Warn("trial-expiry first-invoice failed; in_advance base fee will be deferred",
					"subscription_id", activated.ID,
					"tenant_id", tenantID,
					"error", err)
			}
		}
		// Fire subscription.trial_ended webhook to match the engine
		// auto-flip path. triggered_by="schedule" signals it was the
		// catchup orchestrator (not the operator's EndTrial action).
		if s.events != nil {
			_ = s.events.Dispatch(bound, tenantID, domain.EventSubscriptionTrialEnded, map[string]any{
				"subscription_id": activated.ID,
				"customer_id":     activated.CustomerID,
				"ended_at":        trialEndAt.UTC(),
				"triggered_by":    "schedule",
			})
		}
		processed++
	}
	return processed, batchErrs
}

// ProcessExpiredTrials is the wall-clock cron counterpart to
// ProcessExpiredTrialsForClock — scans non-clock-pinned trialing
// subs whose `trial_end_at` has elapsed in wall-clock time and
// flips each to active at trial_end_at. Same shape as the catchup
// Phase 0.5: ActivateAfterTrial (atomic flip + activated_at) +
// BillOnCreate (in_advance first-paid-period coverage).
//
// Livemode partition comes from ctx (the scheduler fans out per
// mode). Per-row errors are collected but don't abort the batch
// — same shape as every other scheduler-tick processor.
//
// ADR-028 disjoint flows: the store query EXPLICITLY EXCLUDES
// clock-pinned rows (`test_clock_id IS NULL` filter). Those
// flow through the catchup orchestrator's Phase 0.5 instead.
func (s *Service) ProcessExpiredTrials(ctx context.Context, batch int) (int, []error) {
	if batch <= 0 {
		batch = 100
	}
	livemode := postgres.Livemode(ctx)
	now := s.clock.Now(ctx)
	expired, err := s.store.ListExpiredTrials(ctx, now, livemode, batch)
	if err != nil {
		return 0, []error{fmt.Errorf("list expired trials: %w", err)}
	}

	var (
		processed int
		batchErrs []error
	)
	for _, sub := range expired {
		if sub.TrialEndAt == nil {
			continue
		}
		trialEndAt := *sub.TrialEndAt
		activated, err := s.store.ActivateAfterTrial(ctx, sub.TenantID, sub.ID, trialEndAt)
		if err != nil {
			if errors.Is(err, errs.ErrInvalidState) {
				// Operator-EndTrial race (or already-active by some
				// other path) — desired state reached, skip.
				continue
			}
			batchErrs = append(batchErrs, fmt.Errorf("activate sub %s: %w", sub.ID, err))
			continue
		}

		if s.biller != nil {
			if _, err := s.biller.BillOnCreate(ctx, activated); err != nil {
				slog.Warn("trial-expiry first-invoice failed (wall-clock); in_advance base fee will be deferred",
					"subscription_id", activated.ID,
					"tenant_id", activated.TenantID,
					"error", err)
			}
		}
		if s.events != nil {
			_ = s.events.Dispatch(ctx, activated.TenantID, domain.EventSubscriptionTrialEnded, map[string]any{
				"subscription_id": activated.ID,
				"customer_id":     activated.CustomerID,
				"ended_at":        trialEndAt.UTC(),
				"triggered_by":    "schedule",
			})
		}
		processed++
	}
	return processed, batchErrs
}

// ExtendTrial pushes a trialing subscription's trial_end_at later. Used
// when sales/ops grant an existing free-trial customer more time before
// the auto-flip-and-bill fires. newTrialEnd must be strictly in the
// future (compared to the service clock) and strictly after the current
// trial_end_at — shrinking a trial bypasses the operator-intent that
// EndTrial captures, and setting a past timestamp would make the next
// cycle scan flip the sub immediately, which is what EndTrial is for.
// The store atomic enforces status='trialing' so this can't race the
// cycle-scan auto-flip.
func (s *Service) ExtendTrial(ctx context.Context, tenantID, id string, newTrialEnd time.Time) (domain.Subscription, error) {
	ctx = s.bindForSub(ctx, tenantID, id)
	now := s.clock.Now(ctx)
	if !newTrialEnd.After(now) {
		return domain.Subscription{}, errs.Invalid("trial_end", "must be in the future")
	}
	current, err := s.store.Get(ctx, tenantID, id)
	if err != nil {
		return domain.Subscription{}, err
	}
	if current.Status != domain.SubscriptionTrialing {
		return domain.Subscription{}, errs.InvalidState(fmt.Sprintf("cannot extend trial on %s subscription", current.Status))
	}
	if current.TrialEndAt != nil && !newTrialEnd.After(*current.TrialEndAt) {
		return domain.Subscription{}, errs.Invalid("trial_end", "must be after the current trial_end_at — use end-trial to shorten")
	}

	// Re-anchor the first chargeable cycle on the new trial_end. Without
	// this, extending past the old current_period_end silently drops a
	// stub (same bug class as the pre-fix calendar+trial Create branch).
	loc := s.tenantLocation(ctx, tenantID)
	newEnd := newTrialEnd.UTC()
	interval := s.firstPlanInterval(ctx, tenantID, current.Items)
	ps, pe := firstPeriodAfterTrial(newEnd, current.BillingTime, interval, loc)
	return s.store.ExtendTrial(ctx, tenantID, id, newEnd, ps, pe, pe)
}

// ItemThresholdInput is one configured per-item usage cap on a subscription.
// UsageGTE arrives as a JSON string from the wire so meter quantities that
// can be fractional (cached-token ratios, GPU-hours) round-trip without
// float drift. The service parses it into shopspring/decimal before handing
// off to the store.
type ItemThresholdInput struct {
	SubscriptionItemID string `json:"subscription_item_id"`
	UsageGTE           string `json:"usage_gte"`
}

// BillingThresholdsInput is the PATCH body shape for /v1/subscriptions/{id}
// when setting thresholds. AmountGTE is integer cents; ItemThresholds is the
// always-array of per-item caps. Either AmountGTE or ItemThresholds (or both)
// must be set — a body with neither is rejected as no-op.
//
// ResetBillingCycle defaults to true at PATCH time when omitted (matches the
// migration column default and Stripe's reset_billing_cycle behavior). The
// pointer-on-field shape lets the handler distinguish "not supplied" (apply
// default) from "explicitly false".
type BillingThresholdsInput struct {
	AmountGTE         int64                `json:"amount_gte,omitempty"`
	ResetBillingCycle *bool                `json:"reset_billing_cycle,omitempty"`
	ItemThresholds    []ItemThresholdInput `json:"item_thresholds,omitempty"`
}

// SetBillingThresholds writes (amount_gte, reset_cycle, item_thresholds) onto
// a subscription. Validates: at least one of amount_gte or item_thresholds is
// supplied (a body with neither is a no-op masquerade); amount_gte > 0 if
// supplied; usage_gte parses as a non-negative decimal; every
// subscription_item_id in item_thresholds belongs to this subscription;
// duplicate item ids are rejected so the underlying PK doesn't surface as a
// mid-tx integrity error. Multi-currency rejection happens upstream at the
// handler (the only layer with a PlanReader).
//
// Replaces the full set on every call: per-item rows for any item not in the
// new slice are deleted by the store. Idempotent — calling with the same
// input replaces the row's columns and aux rows with the same values.
func (s *Service) SetBillingThresholds(ctx context.Context, tenantID, id string, input BillingThresholdsInput) (domain.Subscription, error) {
	if input.AmountGTE == 0 && len(input.ItemThresholds) == 0 {
		return domain.Subscription{}, errs.Invalid("billing_thresholds",
			"at least one of amount_gte or item_thresholds must be set; to clear use DELETE")
	}
	if input.AmountGTE < 0 {
		return domain.Subscription{}, errs.Invalid("amount_gte", "must be > 0")
	}

	ctx = s.bindForSub(ctx, tenantID, id)
	sub, err := s.store.Get(ctx, tenantID, id)
	if err != nil {
		return domain.Subscription{}, err
	}
	if sub.Status == domain.SubscriptionCanceled || sub.Status == domain.SubscriptionArchived {
		return domain.Subscription{}, errs.InvalidState(
			fmt.Sprintf("cannot configure billing thresholds on %s subscriptions", sub.Status))
	}

	itemIDs := make(map[string]struct{}, len(sub.Items))
	for _, it := range sub.Items {
		itemIDs[it.ID] = struct{}{}
	}

	parsed := make([]domain.SubscriptionItemThreshold, 0, len(input.ItemThresholds))
	seen := make(map[string]struct{}, len(input.ItemThresholds))
	for i, t := range input.ItemThresholds {
		if t.SubscriptionItemID == "" {
			return domain.Subscription{}, errs.Required(fmt.Sprintf("item_thresholds[%d].subscription_item_id", i))
		}
		if _, dup := seen[t.SubscriptionItemID]; dup {
			return domain.Subscription{}, errs.Invalid("item_thresholds",
				fmt.Sprintf("duplicate subscription_item_id %q", t.SubscriptionItemID))
		}
		seen[t.SubscriptionItemID] = struct{}{}
		if _, ok := itemIDs[t.SubscriptionItemID]; !ok {
			return domain.Subscription{}, errs.Invalid(
				fmt.Sprintf("item_thresholds[%d].subscription_item_id", i),
				fmt.Sprintf("item %q does not belong to this subscription", t.SubscriptionItemID))
		}
		usage, derr := decimal.NewFromString(strings.TrimSpace(t.UsageGTE))
		if derr != nil {
			return domain.Subscription{}, errs.Invalid(
				fmt.Sprintf("item_thresholds[%d].usage_gte", i),
				"must be a numeric string (e.g. \"1000\" or \"3.14\")")
		}
		if usage.IsNegative() {
			return domain.Subscription{}, errs.Invalid(
				fmt.Sprintf("item_thresholds[%d].usage_gte", i),
				"must be >= 0")
		}
		parsed = append(parsed, domain.SubscriptionItemThreshold{
			SubscriptionItemID: t.SubscriptionItemID,
			UsageGTE:           usage,
		})
	}

	resetCycle := true
	if input.ResetBillingCycle != nil {
		resetCycle = *input.ResetBillingCycle
	}

	return s.store.SetBillingThresholds(ctx, tenantID, id, domain.BillingThresholds{
		AmountGTE:         input.AmountGTE,
		ResetBillingCycle: resetCycle,
		ItemThresholds:    parsed,
	})
}

// ClearBillingThresholds removes any threshold configuration on a
// subscription. Idempotent — clearing on a sub that has no threshold returns
// the unchanged subscription.
func (s *Service) ClearBillingThresholds(ctx context.Context, tenantID, id string) (domain.Subscription, error) {
	ctx = s.bindForSub(ctx, tenantID, id)
	return s.store.ClearBillingThresholds(ctx, tenantID, id)
}
