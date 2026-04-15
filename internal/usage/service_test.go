package usage

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
)

type memStore struct {
	events map[string]domain.UsageEvent
}

func newMemStore() *memStore {
	return &memStore{events: make(map[string]domain.UsageEvent)}
}

func (m *memStore) Ingest(_ context.Context, tenantID string, e domain.UsageEvent) (domain.UsageEvent, error) {
	if e.IdempotencyKey != "" {
		for _, existing := range m.events {
			if existing.TenantID == tenantID && existing.IdempotencyKey == e.IdempotencyKey {
				return domain.UsageEvent{}, fmt.Errorf("%w: idempotency_key %q", errs.ErrDuplicateKey, e.IdempotencyKey)
			}
		}
	}
	e.ID = fmt.Sprintf("vlx_evt_%d", len(m.events)+1)
	e.TenantID = tenantID
	m.events[e.ID] = e
	return e, nil
}

func (m *memStore) List(_ context.Context, filter ListFilter) ([]domain.UsageEvent, int, error) {
	var result []domain.UsageEvent
	for _, e := range m.events {
		if e.TenantID != filter.TenantID {
			continue
		}
		if filter.CustomerID != "" && e.CustomerID != filter.CustomerID {
			continue
		}
		result = append(result, e)
	}
	return result, len(result), nil
}

func (m *memStore) AggregateForBillingPeriod(_ context.Context, _, _ string, _ []string, _, _ time.Time) (map[string]int64, error) {
	return map[string]int64{}, nil
}

func (m *memStore) AggregateForBillingPeriodByAgg(_ context.Context, _, _ string, _ map[string]string, _, _ time.Time) (map[string]int64, error) {
	return map[string]int64{}, nil
}

func TestIngest(t *testing.T) {
	svc := NewService(newMemStore())
	ctx := context.Background()

	t.Run("valid event", func(t *testing.T) {
		e, err := svc.Ingest(ctx, "t1", IngestInput{
			CustomerID: "cus_1", MeterID: "mtr_1", Quantity: 42,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if e.Quantity != 42 {
			t.Errorf("got quantity %d, want 42", e.Quantity)
		}
		if e.TenantID != "t1" {
			t.Errorf("got tenant_id %q, want t1", e.TenantID)
		}
	})

	t.Run("custom timestamp", func(t *testing.T) {
		ts := time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC)
		e, err := svc.Ingest(ctx, "t1", IngestInput{
			CustomerID: "cus_1", MeterID: "mtr_1", Quantity: 1, Timestamp: &ts,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !e.Timestamp.Equal(ts) {
			t.Errorf("got timestamp %v, want %v", e.Timestamp, ts)
		}
	})

	t.Run("idempotency", func(t *testing.T) {
		_, err := svc.Ingest(ctx, "t1", IngestInput{
			CustomerID: "cus_1", MeterID: "mtr_1", Quantity: 1, IdempotencyKey: "key-1",
		})
		if err != nil {
			t.Fatalf("first ingest failed: %v", err)
		}
		_, err = svc.Ingest(ctx, "t1", IngestInput{
			CustomerID: "cus_1", MeterID: "mtr_1", Quantity: 1, IdempotencyKey: "key-1",
		})
		if err == nil {
			t.Fatal("expected duplicate error")
		}
	})

	t.Run("validation", func(t *testing.T) {
		cases := []IngestInput{
			{MeterID: "m", Quantity: 1},           // missing customer_id
			{CustomerID: "c", Quantity: 1},         // missing meter_id
		}
		for _, input := range cases {
			_, err := svc.Ingest(ctx, "t1", input)
			if err == nil {
				t.Errorf("expected error for %+v", input)
			}
		}
	})
}
