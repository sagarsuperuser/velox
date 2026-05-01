package subscription

import (
	"context"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
)

type Store interface {
	// Create writes a subscription plus its initial items in one transaction.
	// sub.Items is required and must be non-empty. The returned subscription
	// is hydrated with the inserted items (including their assigned IDs).
	Create(ctx context.Context, tenantID string, s domain.Subscription) (domain.Subscription, error)

	// Get, List, and GetDueBilling hydrate the returned subscriptions with
	// their current items via a second query. Callers that need the items
	// should rely on sub.Items directly — a subscription without items is
	// not a valid state and indicates a hydration bug.
	Get(ctx context.Context, tenantID, id string) (domain.Subscription, error)
	List(ctx context.Context, filter ListFilter) ([]domain.Subscription, int, error)
	GetDueBilling(ctx context.Context, before time.Time, limit int) ([]domain.Subscription, error)

	// Update mutates subscription-level columns only (status, billing period,
	// trial, etc). Item mutations use the item-scoped methods below so a
	// partial update to one field doesn't accidentally overwrite an entire
	// item list.
	Update(ctx context.Context, tenantID string, s domain.Subscription) (domain.Subscription, error)
	UpdateBillingCycle(ctx context.Context, tenantID, id string, periodStart, periodEnd, nextBillingAt time.Time) error

	// PauseAtomic, ResumeAtomic, and CancelAtomic execute a conditional
	// UPDATE that only transitions when the row is in an allowed source
	// state. This closes the read-check-write race where two goroutines
	// observing the same "active" status could each apply a different
	// target state across two transactions, producing an outcome neither
	// caller asked for. When the source state does not match, a re-query
	// distinguishes not-found from a stale-status conflict and surfaces
	// the current status in the error message.
	PauseAtomic(ctx context.Context, tenantID, id string) (domain.Subscription, error)
	ResumeAtomic(ctx context.Context, tenantID, id string) (domain.Subscription, error)
	CancelAtomic(ctx context.Context, tenantID, id string) (domain.Subscription, error)

	// ScheduleCancellation persists a future cancel intent. cancelAt is a
	// nullable timestamp; cancelAtPeriodEnd is the soft-cancel flag. Both
	// may be set in the same call (the boundary that fires first wins);
	// the service layer rejects "schedule both at once" so callers don't
	// pass them together in practice.
	ScheduleCancellation(ctx context.Context, tenantID, id string, cancelAt *time.Time, cancelAtPeriodEnd bool) (domain.Subscription, error)

	// ClearScheduledCancellation clears any cancel_at and cancel_at_period_end
	// on the row. Idempotent.
	ClearScheduledCancellation(ctx context.Context, tenantID, id string) (domain.Subscription, error)

	// FireScheduledCancellation atomically transitions a sub to canceled when
	// its scheduled cancel boundary has been crossed. Called by the billing
	// engine cycle scan. Returns errs.InvalidState if the row was no longer
	// active by the time the UPDATE ran.
	FireScheduledCancellation(ctx context.Context, tenantID, id string, at time.Time) (domain.Subscription, error)

	// SetPauseCollection writes the (behavior, resumes_at) pair onto the row.
	// Distinct from PauseAtomic — pause_collection keeps the cycle running
	// but neuters the financial side; the row's status field is not touched.
	// Permitted on any non-terminal status (active, paused, draft); a paused
	// (hard) sub can also have collection paused, and clearing one without
	// the other is supported. Service layer enforces behavior whitelist.
	SetPauseCollection(ctx context.Context, tenantID, id string, pc domain.PauseCollection) (domain.Subscription, error)

	// ClearPauseCollection nulls the pause_collection_* columns. Idempotent
	// — clearing an already-cleared row returns the unchanged subscription.
	ClearPauseCollection(ctx context.Context, tenantID, id string) (domain.Subscription, error)

	// SetBillingThresholds writes the (amount_gte, reset_cycle, item_thresholds)
	// triple onto the row in one transaction. Replaces the full item_thresholds
	// set — the per-item rows for any item not in the new slice are deleted.
	// Rejects rows in canceled/archived since a threshold on a terminated sub
	// has no meaning. The service layer is responsible for validating that
	// every subscription_item_id in t.ItemThresholds belongs to this
	// subscription.
	SetBillingThresholds(ctx context.Context, tenantID, id string, t domain.BillingThresholds) (domain.Subscription, error)

	// ClearBillingThresholds nulls the amount_gte column and deletes every
	// row in subscription_item_thresholds for this subscription. Idempotent
	// — clearing on a sub that has no threshold returns the unchanged
	// subscription.
	ClearBillingThresholds(ctx context.Context, tenantID, id string) (domain.Subscription, error)

	// ListWithThresholds returns subscriptions in the given livemode partition
	// that have at least one threshold configured (amount or per-item) and are
	// in active or trialing status. Used by the threshold scan tick. Result is
	// hydrated with items + thresholds.
	ListWithThresholds(ctx context.Context, livemode bool, limit int) ([]domain.Subscription, error)

	// ActivateAfterTrial atomically transitions a subscription from
	// 'trialing' to 'active'. Sets activated_at = `at` if the column is
	// still NULL (preserves the original activation timestamp on
	// re-runs). Returns errs.InvalidState if the row's status is not
	// 'trialing' at UPDATE time. Called by the billing engine at cycle
	// scan when the trial window has elapsed, and by the operator-facing
	// EndTrial action.
	ActivateAfterTrial(ctx context.Context, tenantID, id string, at time.Time) (domain.Subscription, error)

	// ExtendTrial atomically updates trial_end_at on a 'trialing' row.
	// Returns errs.InvalidState if the row's status is not 'trialing' at
	// UPDATE time (operator already ended the trial, or the row was never
	// trialing). The service layer is responsible for rejecting newTrialEnd
	// values that don't make sense (in the past, or before the existing
	// trial_end_at — those callers should use EndTrial instead).
	ExtendTrial(ctx context.Context, tenantID, id string, newTrialEnd time.Time) (domain.Subscription, error)

	// ---- Subscription items ----

	// ListItems returns all items for a subscription ordered by created_at.
	ListItems(ctx context.Context, tenantID, subscriptionID string) ([]domain.SubscriptionItem, error)

	// GetItem returns a single item by ID, scoped to its parent subscription
	// (callers verify the item belongs to the expected subscription before
	// mutating it).
	GetItem(ctx context.Context, tenantID, itemID string) (domain.SubscriptionItem, error)

	// AddItem appends a new item to a subscription. UNIQUE (subscription_id,
	// plan_id) prevents a second item with the same plan; the constraint
	// violation surfaces as errs.ErrAlreadyExists.
	AddItem(ctx context.Context, tenantID string, item domain.SubscriptionItem) (domain.SubscriptionItem, error)

	// UpdateItemQuantity mutates only the quantity — plan and pending-change
	// fields are left untouched. Used for quantity-only PATCH. Returns the
	// updated item.
	UpdateItemQuantity(ctx context.Context, tenantID, itemID string, quantity int64) (domain.SubscriptionItem, error)

	// ApplyItemPlanImmediately swaps plan_id on the item and stamps
	// plan_changed_at. Used for immediate plan changes. Clears any pending
	// change since the caller just superseded it.
	ApplyItemPlanImmediately(ctx context.Context, tenantID, itemID, newPlanID string, changedAt time.Time) (domain.SubscriptionItem, error)

	// SetItemPendingPlan schedules a plan change on an item.
	SetItemPendingPlan(ctx context.Context, tenantID, itemID, pendingPlanID string, effectiveAt time.Time) (domain.SubscriptionItem, error)

	// ClearItemPendingPlan removes any scheduled plan change on an item.
	// Idempotent — a second call on an item with no pending change returns
	// the unchanged item.
	ClearItemPendingPlan(ctx context.Context, tenantID, itemID string) (domain.SubscriptionItem, error)

	// ApplyDuePendingItemPlansAtomic swaps plan_id ← pending_plan_id for every
	// item under the given subscription whose pending change has come due
	// (pending_plan_effective_at <= now). All applicable items are updated in
	// one statement. Returns the post-swap items. Called by the billing engine
	// at the cycle boundary, mirroring the old ApplyPendingPlanAtomic semantics
	// at item granularity.
	ApplyDuePendingItemPlansAtomic(ctx context.Context, tenantID, subscriptionID string, now time.Time) ([]domain.SubscriptionItem, error)

	// RemoveItem deletes a single item. The service layer is responsible for
	// rejecting removal of the last item on an active subscription (deletion
	// of the last item would leave a subscription with no priced lines,
	// which is not a valid state).
	RemoveItem(ctx context.Context, tenantID, itemID string) error
}

type ListFilter struct {
	TenantID   string
	CustomerID string
	PlanID     string // Joins subscription_items to filter by item plan
	Status     string
	Limit      int
	Offset     int
}
