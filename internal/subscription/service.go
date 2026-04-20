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
		status = domain.SubscriptionActive
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

func beginningOfMonth(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
}
