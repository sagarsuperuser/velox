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
	Aggregate(ctx context.Context, filter ListFilter) (Aggregate, error)
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

	// AggregateDailyBuckets returns events bucketed to UTC-day granularity
	// over [from, to) for the given customer × meter set. One row per
	// (bucket_start, meter_id) — the service fills missing buckets with
	// zero so chart consumers get continuous time. Sums to the same
	// per-meter total as AggregateForBillingPeriod over the same window;
	// powers the daily-bar-chart on the customer-usage view (matches
	// Datadog / OpenAI / AWS Cost Explorer's primary visual primitive).
	AggregateDailyBuckets(ctx context.Context, tenantID, customerID string, meterIDs []string, from, to time.Time) ([]DailyBucketRow, error)
}

// DailyBucketRow is one (bucket_start, meter) cell from the bucket
// aggregation. Storage-shape only; the service composes these into
// the gap-filled DailyBucket presentation type before serving.
type DailyBucketRow struct {
	BucketStart time.Time
	MeterID     string
	Quantity    decimal.Decimal
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

// Aggregate is the response shape of GET /v1/usage-events/aggregate. It
// powers the stat cards + "Usage by Meter" breakdown on the dashboard's
// /usage page so they reflect server-side filtered totals rather than
// reductions over the current page of events.
//
// TotalUnits is decimal-string-encoded (NUMERIC(38,12) per ADR-005) so
// fractional GPU-hours and partial tokens round-trip without loss.
type Aggregate struct {
	TotalEvents     int             `json:"total_events"`
	TotalUnits      decimal.Decimal `json:"total_units"`
	ActiveMeters    int             `json:"active_meters"`
	ActiveCustomers int             `json:"active_customers"`
	ByMeter         []MeterTotal    `json:"by_meter"`
}

// MeterTotal is the per-meter row in Aggregate.ByMeter — one entry per
// distinct meter_id matching the filter, sorted by Total DESC so the
// dashboard's horizontal-bar breakdown can render in priority order.
type MeterTotal struct {
	MeterID string          `json:"meter_id"`
	Total   decimal.Decimal `json:"total"`
}
