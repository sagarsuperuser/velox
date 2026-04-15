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
}

type ListFilter struct {
	TenantID   string
	CustomerID string
	PlanID     string
	Status     string
	Limit      int
	Offset     int
}
