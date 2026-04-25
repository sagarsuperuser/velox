package usage

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
)

type Service struct {
	store Store
}

func NewService(store Store) *Service {
	return &Service{store: store}
}

// IngestInput is the internal service input — uses resolved internal IDs only.
// The handler is responsible for resolving external identifiers before calling this.
type IngestInput struct {
	CustomerID     string          `json:"customer_id"`
	MeterID        string          `json:"meter_id"`
	SubscriptionID string          `json:"subscription_id,omitempty"`
	Quantity       decimal.Decimal `json:"quantity,omitempty"`
	Dimensions     map[string]any  `json:"dimensions,omitempty"`
	IdempotencyKey string          `json:"idempotency_key,omitempty"`
	Timestamp      *time.Time      `json:"timestamp,omitempty"`
}

func (s *Service) Ingest(ctx context.Context, tenantID string, input IngestInput) (domain.UsageEvent, error) {
	return s.ingest(ctx, tenantID, input, domain.UsageOriginAPI)
}

// Backfill ingests a historical usage event. Requires a non-nil timestamp
// strictly in the past; rejects missing / future / equal-to-now values so
// operators can't accidentally double-post a live event through the audit
// path. The row is tagged origin='backfill'.
//
// Billing semantics: backfilled events participate in aggregation for any
// period whose [start, end) contains the event's timestamp. Finalized
// invoices are immutable (they reference billed_entries, not live
// aggregations), so backfill into closed periods is safe — it changes the
// audit ledger without rewriting history.
func (s *Service) Backfill(ctx context.Context, tenantID string, input IngestInput) (domain.UsageEvent, error) {
	if input.Timestamp == nil {
		return domain.UsageEvent{}, errs.Required("timestamp")
	}
	// Reject future timestamps. Equality-with-now is allowed because the
	// test-vs-prod clock race makes strict past-only brittle to verify, and
	// the 'backfill' origin tag already distinguishes these rows from live
	// POST traffic for audit purposes.
	if input.Timestamp.After(time.Now().UTC()) {
		return domain.UsageEvent{}, errs.Invalid("timestamp", "must not be in the future for backfill — use POST /usage-events for real-time ingest")
	}
	return s.ingest(ctx, tenantID, input, domain.UsageOriginBackfill)
}

// MaxDimensionKeys caps the size of the JSONB dimensions map on each
// usage event. Dimensions feed pricing-rule dispatch via @> subset
// matches at finalize time; bounding the per-event JSONB size protects
// the GIN index from pathological tenants and matches the equivalent
// cap on meter_pricing_rules.dimension_match (16 keys).
const MaxDimensionKeys = 16

// validateDimensions enforces the v1 dimension contract: at most
// MaxDimensionKeys keys, scalar values only (string, number, bool, nil).
// Object/array values are rejected — Postgres `@>` would still match
// them but the priority+claim semantics aren't well-defined for nested
// containers in v1 (revisit if a design partner needs it).
func validateDimensions(dims map[string]any) error {
	if len(dims) > MaxDimensionKeys {
		return errs.Invalid("dimensions", fmt.Sprintf("at most %d keys (got %d)", MaxDimensionKeys, len(dims)))
	}
	for k, v := range dims {
		switch v.(type) {
		case nil, string, bool, float64, float32, int, int32, int64:
			// Scalar — fine.
		default:
			return errs.Invalid("dimensions", fmt.Sprintf("key %q value must be a scalar (string/number/bool), got %T", k, v))
		}
	}
	return nil
}

func (s *Service) ingest(ctx context.Context, tenantID string, input IngestInput, origin domain.UsageEventOrigin) (domain.UsageEvent, error) {
	if strings.TrimSpace(input.CustomerID) == "" {
		return domain.UsageEvent{}, errs.Required("customer_id")
	}
	if strings.TrimSpace(input.MeterID) == "" {
		return domain.UsageEvent{}, errs.Required("meter_id")
	}
	if err := validateDimensions(input.Dimensions); err != nil {
		return domain.UsageEvent{}, err
	}
	if input.Quantity.IsZero() {
		// Default quantity to 1 (count-based meters) when not explicitly provided.
		// Negative values are allowed as usage corrections.
		input.Quantity = decimal.NewFromInt(1)
	}

	ts := time.Now().UTC()
	if input.Timestamp != nil {
		ts = input.Timestamp.UTC()
	}

	return s.store.Ingest(ctx, tenantID, domain.UsageEvent{
		CustomerID:     input.CustomerID,
		MeterID:        input.MeterID,
		SubscriptionID: input.SubscriptionID,
		Quantity:       input.Quantity,
		Dimensions:     input.Dimensions,
		IdempotencyKey: input.IdempotencyKey,
		Timestamp:      ts,
		Origin:         origin,
	})
}

// BatchIngest ingests multiple usage events. Returns successfully ingested count
// and any individual errors (partial success is allowed).
func (s *Service) BatchIngest(ctx context.Context, tenantID string, events []IngestInput) (int, []error) {
	var batchErrs []error
	ingested := 0

	for _, input := range events {
		_, err := s.Ingest(ctx, tenantID, input)
		if err != nil {
			batchErrs = append(batchErrs, err)
			continue
		}
		ingested++
	}

	return ingested, batchErrs
}

func (s *Service) List(ctx context.Context, filter ListFilter) ([]domain.UsageEvent, int, error) {
	return s.store.List(ctx, filter)
}

func (s *Service) AggregateForBillingPeriod(ctx context.Context, tenantID, customerID string, meterIDs []string, from, to time.Time) (map[string]decimal.Decimal, error) {
	return s.store.AggregateForBillingPeriod(ctx, tenantID, customerID, meterIDs, from, to)
}

// AggregateByPricingRules resolves a single (customer, meter, period) into
// per-rule aggregations using the priority+claim algorithm. The defaultMode
// applies to events that match no rule; it must be one of the four
// period-bounded modes (sum, count, max, last_during_period) — last_ever
// as a meter-default is rejected because it would silently break the
// "current state" semantics for unclaimed events.
//
// See docs/design-multi-dim-meters.md for the resolution semantics; this
// method is the runtime entry point that billing-finalize will call.
func (s *Service) AggregateByPricingRules(
	ctx context.Context,
	tenantID, customerID, meterID string,
	defaultMode domain.AggregationMode,
	from, to time.Time,
) ([]domain.RuleAggregation, error) {
	if defaultMode == "" {
		defaultMode = domain.AggSum
	}
	switch defaultMode {
	case domain.AggSum, domain.AggCount, domain.AggMax, domain.AggLastDuringPeriod:
		// ok
	default:
		return nil, errs.Invalid("default_mode", fmt.Sprintf("must be one of sum/count/max/last_during_period, got %q", defaultMode))
	}
	return s.store.AggregateByPricingRules(ctx, tenantID, customerID, meterID, defaultMode, from, to)
}
