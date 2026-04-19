package subscription

import (
	"context"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
)

type Store interface {
	Create(ctx context.Context, tenantID string, s domain.Subscription) (domain.Subscription, error)
	Get(ctx context.Context, tenantID, id string) (domain.Subscription, error)
	List(ctx context.Context, filter ListFilter) ([]domain.Subscription, int, error)
	Update(ctx context.Context, tenantID string, s domain.Subscription) (domain.Subscription, error)
	GetDueBilling(ctx context.Context, before time.Time, limit int) ([]domain.Subscription, error)
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

	// SetPendingPlan records a scheduled plan change. Writes pending_plan_id
	// and pending_plan_effective_at; leaves plan_id untouched. ChangePlan with
	// immediate=false uses this so the API's effective_at response is honest —
	// the current plan keeps running until the billing engine swaps it atomically
	// at the cycle boundary via ApplyPendingPlanAtomic.
	SetPendingPlan(ctx context.Context, tenantID, id, pendingPlanID string, effectiveAt time.Time) (domain.Subscription, error)

	// ClearPendingPlan removes any scheduled plan change. Used when a customer
	// cancels the pending change before it takes effect, or when a subscription
	// is canceled outright (pending change no longer applicable).
	ClearPendingPlan(ctx context.Context, tenantID, id string) (domain.Subscription, error)

	// ApplyPendingPlanAtomic swaps the plan iff a pending change exists and its
	// effective_at is reached. In one UPDATE: plan_id ← pending_plan_id,
	// previous_plan_id ← current plan_id, plan_changed_at ← now, and both
	// pending columns cleared. Conditional so a concurrent ClearPendingPlan
	// (customer cancels the change at the same cycle boundary the engine tries
	// to apply it) can't produce a double-apply or a lost cancel — whichever
	// statement commits first wins; the loser sees "no rows" and treats the
	// pending change as already handled.
	ApplyPendingPlanAtomic(ctx context.Context, tenantID, id string, now time.Time) (domain.Subscription, error)
}

type ListFilter struct {
	TenantID   string
	CustomerID string
	PlanID     string
	Status     string
	Limit      int
	Offset     int
}
