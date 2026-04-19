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
}

type ListFilter struct {
	TenantID   string
	CustomerID string
	PlanID     string
	Status     string
	Limit      int
	Offset     int
}
