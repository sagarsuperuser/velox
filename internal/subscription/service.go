package subscription

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/clock"
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

// Biller is the narrow shape used after Create to emit the day-1
// invoice for an in_advance subscription (ADR-031). Optional —
// nil-safe; without it, in_advance subs silently behave like
// in_arrears until the next cycle close. Implemented by *billing.Engine.
type Biller interface {
	BillOnCreate(ctx context.Context, sub domain.Subscription) (domain.Invoice, error)
}

type Service struct {
	store     Store
	clock     clock.Clock
	settings  SettingsReader
	customers CustomerReader
	biller    Biller
	resolver  clock.Resolver
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
		if billingTime == domain.BillingTimeCalendar {
			// Trial ends → period starts on first of next month after
			// trial-end, snapped to 00:00 tenant TZ.
			ps := beginningOfMonthIn(te.AddDate(0, 1, 0), loc)
			pe := beginningOfMonthIn(ps.AddDate(0, 1, 0), loc)
			periodStart = &ps
			periodEnd = &pe
			nextBilling = &pe
		} else {
			// Anniversary: period starts at trial-end snapped to 00:00.
			ps := beginningOfDayIn(te, loc)
			pe := ps.AddDate(0, 1, 0)
			periodStart = &ps
			periodEnd = &pe
			nextBilling = &pe
		}
	} else if input.StartNow {
		status = domain.SubscriptionActive
		startedAt = &now
		if billingTime == domain.BillingTimeCalendar {
			// Today's start-of-day in tenant TZ → first of next month
			// at 00:00 in tenant TZ. A sub created at any time on May 1
			// gets the full first day of May.
			ps := beginningOfDayIn(now, loc)
			pe := beginningOfMonthIn(now.AddDate(0, 1, 0), loc)
			periodStart = &ps
			periodEnd = &pe
			nextBilling = &pe
		} else {
			ps := beginningOfDayIn(now, loc)
			pe := ps.AddDate(0, 1, 0)
			periodStart = &ps
			periodEnd = &pe
			nextBilling = &pe
		}
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
		loc := s.tenantLocation(ctx, tenantID)
		ps := beginningOfMonthIn(now, loc)
		pe := beginningOfMonthIn(now.AddDate(0, 1, 0), loc)
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
	return s.store.RemoveItem(ctx, tenantID, itemID)
}

// Pause pauses an active subscription. Can be resumed later.
func (s *Service) Pause(ctx context.Context, tenantID, id string) (domain.Subscription, error) {
	return s.store.PauseAtomic(ctx, tenantID, id)
}

// Resume resumes a paused subscription.
func (s *Service) Resume(ctx context.Context, tenantID, id string) (domain.Subscription, error) {
	return s.store.ResumeAtomic(ctx, tenantID, id)
}

func (s *Service) Cancel(ctx context.Context, tenantID, id string) (domain.Subscription, error) {
	return s.store.CancelAtomic(ctx, tenantID, id)
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

	pc := domain.PauseCollection{Behavior: input.Behavior}
	if input.ResumesAt != nil {
		ts := input.ResumesAt.UTC()
		ctx = s.bindForSub(ctx, tenantID, id)
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
	return s.store.ActivateAfterTrial(ctx, tenantID, id, now)
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
	return s.store.ExtendTrial(ctx, tenantID, id, newTrialEnd.UTC())
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
	return s.store.ClearBillingThresholds(ctx, tenantID, id)
}

