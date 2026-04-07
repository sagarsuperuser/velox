package usage

import (
	"context"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
)

type Store interface {
	Ingest(ctx context.Context, tenantID string, event domain.UsageEvent) (domain.UsageEvent, error)
	List(ctx context.Context, filter ListFilter) ([]domain.UsageEvent, error)
	AggregateForBillingPeriod(ctx context.Context, tenantID, subscriptionID string, meterIDs []string, from, to time.Time) (map[string]int64, error)
}

type ListFilter struct {
	TenantID   string
	CustomerID string
	MeterID    string
	From       *time.Time
	To         *time.Time
	Limit      int
	Offset     int
}
