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

type IngestInput struct {
	CustomerID     string         `json:"customer_id"`
	MeterID        string         `json:"meter_id"`
	SubscriptionID string         `json:"subscription_id,omitempty"`
	Quantity       int64          `json:"quantity"`
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
	if input.Quantity <= 0 {
		return domain.UsageEvent{}, fmt.Errorf("quantity must be > 0")
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

func (s *Service) List(ctx context.Context, filter ListFilter) ([]domain.UsageEvent, error) {
	return s.store.List(ctx, filter)
}

func (s *Service) AggregateForBillingPeriod(ctx context.Context, tenantID, subscriptionID string, meterIDs []string, from, to time.Time) (map[string]int64, error) {
	return s.store.AggregateForBillingPeriod(ctx, tenantID, subscriptionID, meterIDs, from, to)
}
