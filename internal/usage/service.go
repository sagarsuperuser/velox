package usage

import (
	"context"
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
	Properties     map[string]any  `json:"properties,omitempty"`
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

func (s *Service) ingest(ctx context.Context, tenantID string, input IngestInput, origin domain.UsageEventOrigin) (domain.UsageEvent, error) {
	if strings.TrimSpace(input.CustomerID) == "" {
		return domain.UsageEvent{}, errs.Required("customer_id")
	}
	if strings.TrimSpace(input.MeterID) == "" {
		return domain.UsageEvent{}, errs.Required("meter_id")
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
		Properties:     input.Properties,
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
