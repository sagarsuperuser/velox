package usage

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
)

func dec(n int64) decimal.Decimal { return decimal.NewFromInt(n) }

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

func (m *memStore) AggregateForBillingPeriod(_ context.Context, _, _ string, _ []string, _, _ time.Time) (map[string]decimal.Decimal, error) {
	return map[string]decimal.Decimal{}, nil
}

func (m *memStore) AggregateForBillingPeriodByAgg(_ context.Context, _, _ string, _ map[string]string, _, _ time.Time) (map[string]decimal.Decimal, error) {
	return map[string]decimal.Decimal{}, nil
}

func TestIngest(t *testing.T) {
	svc := NewService(newMemStore())
	ctx := context.Background()

	t.Run("valid event", func(t *testing.T) {
		e, err := svc.Ingest(ctx, "t1", IngestInput{
			CustomerID: "cus_1", MeterID: "mtr_1", Quantity: dec(42),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !e.Quantity.Equal(dec(42)) {
			t.Errorf("got quantity %s, want 42", e.Quantity.String())
		}
		if e.TenantID != "t1" {
			t.Errorf("got tenant_id %q, want t1", e.TenantID)
		}
	})

	t.Run("custom timestamp", func(t *testing.T) {
		ts := time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC)
		e, err := svc.Ingest(ctx, "t1", IngestInput{
			CustomerID: "cus_1", MeterID: "mtr_1", Quantity: dec(1), Timestamp: &ts,
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
			CustomerID: "cus_1", MeterID: "mtr_1", Quantity: dec(1), IdempotencyKey: "key-1",
		})
		if err != nil {
			t.Fatalf("first ingest failed: %v", err)
		}
		_, err = svc.Ingest(ctx, "t1", IngestInput{
			CustomerID: "cus_1", MeterID: "mtr_1", Quantity: dec(1), IdempotencyKey: "key-1",
		})
		if err == nil {
			t.Fatal("expected duplicate error")
		}
	})

	t.Run("validation", func(t *testing.T) {
		cases := []IngestInput{
			{MeterID: "m", Quantity: dec(1)},    // missing customer_id
			{CustomerID: "c", Quantity: dec(1)}, // missing meter_id
		}
		for _, input := range cases {
			_, err := svc.Ingest(ctx, "t1", input)
			if err == nil {
				t.Errorf("expected error for %+v", input)
			}
		}
	})

	t.Run("default origin is api", func(t *testing.T) {
		e, err := svc.Ingest(ctx, "t1", IngestInput{
			CustomerID: "cus_origin_api", MeterID: "mtr_1", Quantity: dec(1),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if e.Origin != domain.UsageOriginAPI {
			t.Errorf("got origin %q, want %q", e.Origin, domain.UsageOriginAPI)
		}
	})
}

// TestBackfill exercises the audit-path contract: past timestamp required,
// future / missing timestamp rejected, origin tagged 'backfill' on the row.
func TestBackfill(t *testing.T) {
	svc := NewService(newMemStore())
	ctx := context.Background()

	t.Run("past timestamp is accepted and tagged backfill", func(t *testing.T) {
		past := time.Now().Add(-24 * time.Hour)
		e, err := svc.Backfill(ctx, "t1", IngestInput{
			CustomerID: "cus_1", MeterID: "mtr_1", Quantity: dec(7), Timestamp: &past,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if e.Origin != domain.UsageOriginBackfill {
			t.Errorf("origin: got %q, want %q", e.Origin, domain.UsageOriginBackfill)
		}
		if !e.Timestamp.Equal(past.UTC()) {
			t.Errorf("timestamp: got %v, want %v", e.Timestamp, past.UTC())
		}
	})

	t.Run("missing timestamp rejected", func(t *testing.T) {
		_, err := svc.Backfill(ctx, "t1", IngestInput{
			CustomerID: "cus_1", MeterID: "mtr_1", Quantity: dec(1),
		})
		if err == nil {
			t.Fatal("expected timestamp-required error")
		}
	})

	t.Run("future timestamp rejected", func(t *testing.T) {
		future := time.Now().Add(1 * time.Hour)
		_, err := svc.Backfill(ctx, "t1", IngestInput{
			CustomerID: "cus_1", MeterID: "mtr_1", Quantity: dec(1), Timestamp: &future,
		})
		if err == nil {
			t.Fatal("expected future-timestamp error")
		}
	})

	t.Run("one-second-ago accepted", func(t *testing.T) {
		ts := time.Now().Add(-1 * time.Second)
		_, err := svc.Backfill(ctx, "t1", IngestInput{
			CustomerID: "cus_boundary", MeterID: "mtr_1", Quantity: dec(1), Timestamp: &ts,
		})
		if err != nil {
			t.Fatalf("one-second-ago backfill should be accepted: %v", err)
		}
	})

	t.Run("missing customer_id still caught", func(t *testing.T) {
		past := time.Now().Add(-1 * time.Hour)
		_, err := svc.Backfill(ctx, "t1", IngestInput{
			MeterID: "mtr_1", Quantity: dec(1), Timestamp: &past,
		})
		if err == nil {
			t.Fatal("expected missing customer_id error")
		}
	})
}
