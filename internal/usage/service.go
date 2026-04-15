package usage

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
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
	CustomerID     string         `json:"customer_id"`
	MeterID        string         `json:"meter_id"`
	SubscriptionID string         `json:"subscription_id,omitempty"`
	Quantity       int64          `json:"quantity,omitempty"`
	Properties     map[string]any `json:"properties,omitempty"`
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
	Timestamp      *time.Time     `json:"timestamp,omitempty"`
}

func (s *Service) Ingest(ctx context.Context, tenantID string, input IngestInput) (domain.UsageEvent, error) {
	if strings.TrimSpace(input.CustomerID) == "" {
		return domain.UsageEvent{}, fmt.Errorf("customer_id is required")
	}
	if strings.TrimSpace(input.MeterID) == "" {
		return domain.UsageEvent{}, fmt.Errorf("meter_id is required")
	}
	if input.Quantity == 0 {
		// Default quantity to 1 (count-based meters) when not explicitly provided.
		// Negative values are allowed as usage corrections.
		input.Quantity = 1
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
	})
}

// BatchIngest ingests multiple usage events. Returns successfully ingested count
// and any individual errors (partial success is allowed).
func (s *Service) BatchIngest(ctx context.Context, tenantID string, events []IngestInput) (int, []error) {
	var errs []error
	ingested := 0

	for _, input := range events {
		_, err := s.Ingest(ctx, tenantID, input)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		ingested++
	}

	return ingested, errs
}

func (s *Service) List(ctx context.Context, filter ListFilter) ([]domain.UsageEvent, int, error) {
	return s.store.List(ctx, filter)
}

func (s *Service) AggregateForBillingPeriod(ctx context.Context, tenantID, customerID string, meterIDs []string, from, to time.Time) (map[string]int64, error) {
	return s.store.AggregateForBillingPeriod(ctx, tenantID, customerID, meterIDs, from, to)
}
