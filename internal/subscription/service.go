package subscription

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/clock"
)

var slugPattern = regexp.MustCompile(`^[a-zA-Z0-9_\-]+$`)

type Service struct {
	store Store
	clock clock.Clock
}

func NewService(store Store, clk clock.Clock) *Service {
	if clk == nil {
		clk = clock.Real()
	}
	return &Service{store: store, clock: clk}
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
	TestClockID   string                         `json:"test_clock_id,omitempty"`
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

	// test_clock_id is a test-mode-only affordance; the DB CHECK constraint
	// would reject it, but we surface a 400 up-front rather than leaking a
	// cryptic integrity error to the API caller.
	if input.TestClockID != "" && auth.Livemode(ctx) {
		return domain.Subscription{}, errs.Invalid("test_clock_id", "test_clock_id is only permitted on test-mode subscriptions")
	}

	status := domain.SubscriptionDraft
	now := s.clock.Now()

	var trialStart, trialEnd *time.Time
	var startedAt *time.Time

	var periodStart, periodEnd, nextBilling *time.Time

	if input.TrialDays > 0 {
		ts := now
		te := now.AddDate(0, 0, input.TrialDays)
		trialStart = &ts
		trialEnd = &te
		status = domain.SubscriptionTrialing
		startedAt = &now
		if billingTime == domain.BillingTimeCalendar {
			ps := beginningOfMonth(te.AddDate(0, 1, 0))
			pe := ps.AddDate(0, 1, 0)
			periodStart = &ps
			periodEnd = &pe
			nextBilling = &pe
		} else {
			ps := te
			pe := te.AddDate(0, 1, 0)
			periodStart = &ps
			periodEnd = &pe
			nextBilling = &pe
		}
	} else if input.StartNow {
		status = domain.SubscriptionActive
		startedAt = &now
		if billingTime == domain.BillingTimeCalendar {
			ps := now
			pe := beginningOfMonth(now).AddDate(0, 1, 0)
			periodStart = &ps
			periodEnd = &pe
			nextBilling = &pe
		} else {
			ps := now
			pe := now.AddDate(0, 1, 0)
			periodStart = &ps
			periodEnd = &pe
			nextBilling = &pe
		}
	}

	overageAction := input.OverageAction
	if overageAction == "" {
		overageAction = "charge"
	}

	return s.store.Create(ctx, tenantID, domain.Subscription{
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
		TestClockID:               input.TestClockID,
		Items:                     items,
	})
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

	now := s.clock.Now()
	sub.Status = domain.SubscriptionActive
	sub.ActivatedAt = &now
	sub.StartedAt = &now

	if sub.CurrentBillingPeriodStart == nil {
		ps := beginningOfMonth(now)
		pe := ps.AddDate(0, 1, 0)
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

	now := s.clock.Now()

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

	now := s.clock.Now()
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
		if !ts.After(s.clock.Now()) {
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
	now := s.clock.Now()
	return s.store.ActivateAfterTrial(ctx, tenantID, id, now)
}

func beginningOfMonth(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
}
