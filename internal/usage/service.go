package usage

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// CustomerResolver looks up a customer by external ID.
type CustomerResolver interface {
	GetByExternalID(ctx context.Context, tenantID, externalID string) (domain.Customer, error)
}

// MeterResolver looks up a meter by key.
type MeterResolver interface {
	GetMeterByKey(ctx context.Context, tenantID, key string) (domain.Meter, error)
}

type Service struct {
	store     Store
	customers CustomerResolver
	meters    MeterResolver
}

func NewService(store Store, customers CustomerResolver, meters MeterResolver) *Service {
	return &Service{store: store, customers: customers, meters: meters}
}

type IngestInput struct {
	// Primary identifiers (internal IDs)
	CustomerID     string         `json:"customer_id,omitempty"`
	MeterID        string         `json:"meter_id,omitempty"`
	// Developer-friendly identifiers (resolved to internal IDs)
	ExternalCustomerID string     `json:"external_customer_id,omitempty"`
	EventName          string     `json:"event_name,omitempty"`

	SubscriptionID string         `json:"subscription_id,omitempty"`
	Quantity       int64          `json:"quantity,omitempty"`
	Properties     map[string]any `json:"properties,omitempty"`
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
	Timestamp      *time.Time     `json:"timestamp,omitempty"`
}

func (s *Service) Ingest(ctx context.Context, tenantID string, input IngestInput) (domain.UsageEvent, error) {
	// Resolve external_customer_id → customer_id
	if strings.TrimSpace(input.CustomerID) == "" && strings.TrimSpace(input.ExternalCustomerID) != "" {
		cust, err := s.customers.GetByExternalID(ctx, tenantID, strings.TrimSpace(input.ExternalCustomerID))
		if err != nil {
			return domain.UsageEvent{}, fmt.Errorf("customer with external_id %q not found", input.ExternalCustomerID)
		}
		input.CustomerID = cust.ID
	}
	// Resolve event_name → meter_id
	if strings.TrimSpace(input.MeterID) == "" && strings.TrimSpace(input.EventName) != "" {
		meter, err := s.meters.GetMeterByKey(ctx, tenantID, strings.TrimSpace(input.EventName))
		if err != nil {
			return domain.UsageEvent{}, fmt.Errorf("meter with key %q not found", input.EventName)
		}
		input.MeterID = meter.ID
	}

	if strings.TrimSpace(input.CustomerID) == "" {
		return domain.UsageEvent{}, fmt.Errorf("customer_id or external_customer_id is required")
	}
	if strings.TrimSpace(input.MeterID) == "" {
		return domain.UsageEvent{}, fmt.Errorf("meter_id or event_name is required")
	}
	// Default quantity to 1 (count-based meters)
	if input.Quantity <= 0 {
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

func (s *Service) List(ctx context.Context, filter ListFilter) ([]domain.UsageEvent, error) {
	return s.store.List(ctx, filter)
}

func (s *Service) AggregateForBillingPeriod(ctx context.Context, tenantID, subscriptionID string, meterIDs []string, from, to time.Time) (map[string]int64, error) {
	return s.store.AggregateForBillingPeriod(ctx, tenantID, subscriptionID, meterIDs, from, to)
}
