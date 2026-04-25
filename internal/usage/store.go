package usage

import (
	"context"
	"time"

	"github.com/shopspring/decimal"

	"github.com/sagarsuperuser/velox/internal/domain"
)

type Store interface {
	Ingest(ctx context.Context, tenantID string, event domain.UsageEvent) (domain.UsageEvent, error)
	List(ctx context.Context, filter ListFilter) ([]domain.UsageEvent, int, error)
	AggregateForBillingPeriod(ctx context.Context, tenantID, customerID string, meterIDs []string, from, to time.Time) (map[string]decimal.Decimal, error)
	AggregateForBillingPeriodByAgg(ctx context.Context, tenantID, customerID string, meters map[string]string, from, to time.Time) (map[string]decimal.Decimal, error)

	// AggregateByPricingRules walks meter_pricing_rules in priority-DESC
	// order and claims each in-period event to its top-priority matching
	// rule (JSONB superset on properties), then aggregates per rule using
	// the rule's aggregation_mode. Events that match no rule are returned
	// as an unclaimed entry (RuleID=="") aggregated with defaultMode.
	//
	// last_ever rules ignore the period bounds and pick the latest event
	// across all time (for "current state" billing like seat counts).
	AggregateByPricingRules(ctx context.Context, tenantID, customerID, meterID string, defaultMode domain.AggregationMode, from, to time.Time) ([]domain.RuleAggregation, error)
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
