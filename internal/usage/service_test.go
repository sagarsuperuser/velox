package usage

import (
	"context"
	"fmt"
	"strings"
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

// listClamp mirrors the PostgresStore.List cap (it silently clamps the
// requested limit to 1000). GetSummary must NOT read its totals from
// List, or any customer with more events than this in the window is
// under-counted. Keeping the fake honest about the clamp is what makes
// the GetSummary regression test meaningful.
const listClamp = 1000

func (m *memStore) matching(filter ListFilter) []domain.UsageEvent {
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
	return result
}

func (m *memStore) List(_ context.Context, filter ListFilter) ([]domain.UsageEvent, int, error) {
	result := m.matching(filter)
	total := len(result)
	if len(result) > listClamp {
		result = result[:listClamp]
	}
	return result, total, nil
}

// Aggregate reduces the full filtered set server-side (no clamp) — the
// shape PostgresStore.Aggregate produces via COUNT(*) + GROUP BY SUM.
func (m *memStore) Aggregate(_ context.Context, filter ListFilter) (Aggregate, error) {
	events := m.matching(filter)
	byMeter := make(map[string]decimal.Decimal)
	order := []string{}
	for _, e := range events {
		if _, seen := byMeter[e.MeterID]; !seen {
			order = append(order, e.MeterID)
		}
		byMeter[e.MeterID] = byMeter[e.MeterID].Add(e.Quantity)
	}
	agg := Aggregate{TotalEvents: len(events), ByMeter: []MeterTotal{}}
	for _, id := range order {
		agg.ByMeter = append(agg.ByMeter, MeterTotal{MeterID: id, Total: byMeter[id]})
	}
	return agg, nil
}

func (m *memStore) AggregateForBillingPeriod(_ context.Context, _, _ string, _ []string, _, _ time.Time) (map[string]decimal.Decimal, error) {
	return map[string]decimal.Decimal{}, nil
}

func (m *memStore) AggregateForBillingPeriodByAgg(_ context.Context, _, _ string, _ map[string]string, _, _ time.Time) (map[string]decimal.Decimal, error) {
	return map[string]decimal.Decimal{}, nil
}

func (m *memStore) AggregateByPricingRules(_ context.Context, _, _, _ string, _ domain.AggregationMode, _, _ time.Time) ([]domain.RuleAggregation, error) {
	return nil, nil
}

func (m *memStore) AggregateDailyBuckets(_ context.Context, _, _ string, _ []string, _, _ time.Time) ([]DailyBucketRow, error) {
	return nil, nil
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

	t.Run("rejects future timestamp on live ingest", func(t *testing.T) {
		future := time.Now().UTC().Add(2 * time.Hour)
		_, err := svc.Ingest(ctx, "t1", IngestInput{
			CustomerID: "cus_fut", MeterID: "mtr_1", Quantity: dec(1), Timestamp: &future,
		})
		if err == nil {
			t.Error("expected rejection for future-dated live usage event")
		}
	})

	t.Run("allows near-now timestamp within skew", func(t *testing.T) {
		near := time.Now().UTC().Add(1 * time.Minute) // within usageFutureSkew
		_, err := svc.Ingest(ctx, "t1", IngestInput{
			CustomerID: "cus_near", MeterID: "mtr_1", Quantity: dec(1), Timestamp: &near,
		})
		if err != nil {
			t.Errorf("near-now timestamp within skew should be accepted, got: %v", err)
		}
	})

	t.Run("rejects quantity exceeding NUMERIC(38,12) envelope", func(t *testing.T) {
		huge := decimal.New(1, 26) // 10^26 — one past the 26-integer-digit limit
		_, err := svc.Ingest(ctx, "t1", IngestInput{
			CustomerID: "cus_big", MeterID: "mtr_1", Quantity: huge,
		})
		if err == nil {
			t.Error("expected 422-style rejection for over-magnitude quantity (would 500 on INSERT)")
		}
		// A large-but-representable value (just under the bound) is accepted.
		ok := decimal.New(999, 23) // 9.99e25 < 10^26
		if _, err := svc.Ingest(ctx, "t1", IngestInput{
			CustomerID: "cus_ok_big", MeterID: "mtr_1", Quantity: ok,
		}); err != nil {
			t.Errorf("representable large quantity should be accepted, got: %v", err)
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

	t.Run("dimensions persist round-trip", func(t *testing.T) {
		e, err := svc.Ingest(ctx, "t1", IngestInput{
			CustomerID: "cus_dims", MeterID: "mtr_1", Quantity: dec(1),
			Dimensions: map[string]any{"model": "gpt-4", "cached": false, "tier": int64(1)},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if e.Dimensions["model"] != "gpt-4" {
			t.Errorf("model: got %v", e.Dimensions["model"])
		}
		if e.Dimensions["cached"] != false {
			t.Errorf("cached: got %v", e.Dimensions["cached"])
		}
	})

	t.Run("rejects too many dimension keys", func(t *testing.T) {
		dims := map[string]any{}
		for i := 0; i < MaxDimensionKeys+1; i++ {
			dims[fmt.Sprintf("k%d", i)] = "v"
		}
		_, err := svc.Ingest(ctx, "t1", IngestInput{
			CustomerID: "c", MeterID: "m", Quantity: dec(1), Dimensions: dims,
		})
		if err == nil {
			t.Fatal("expected error for too many keys")
		}
	})

	t.Run("rejects non-scalar dimension value", func(t *testing.T) {
		_, err := svc.Ingest(ctx, "t1", IngestInput{
			CustomerID: "c", MeterID: "m", Quantity: dec(1),
			Dimensions: map[string]any{"models": []string{"gpt-4", "claude"}},
		})
		if err == nil {
			t.Fatal("expected error for slice value")
		}
	})
}

// TestAggregateByPricingRules_DefaultModeValidation guards the service-
// level rule that the unclaimed-bucket fallback mode must be one of the
// four period-bounded modes. last_ever as a default would silently break
// "current state" semantics for events that match no rule, so the
// service rejects it before the SQL ever runs.
func TestAggregateByPricingRules_DefaultModeValidation(t *testing.T) {
	svc := NewService(newMemStore())
	ctx := context.Background()
	from := time.Now().Add(-1 * time.Hour)
	to := time.Now().Add(1 * time.Hour)

	t.Run("rejects last_ever as default mode", func(t *testing.T) {
		_, err := svc.AggregateByPricingRules(ctx, "t1", "c1", "m1", domain.AggLastEver, from, to)
		if err == nil {
			t.Fatal("expected default_mode=last_ever to be rejected")
		}
	})

	t.Run("rejects unknown mode", func(t *testing.T) {
		_, err := svc.AggregateByPricingRules(ctx, "t1", "c1", "m1", domain.AggregationMode("bogus"), from, to)
		if err == nil {
			t.Fatal("expected unknown mode to be rejected")
		}
	})

	t.Run("empty mode defaults to sum", func(t *testing.T) {
		_, err := svc.AggregateByPricingRules(ctx, "t1", "c1", "m1", "", from, to)
		if err != nil {
			t.Fatalf("empty mode should default to sum: %v", err)
		}
	})
}

// TestGetSummaryServerSideAggregate is the regression guard for the
// under-count bug: GetSummary used List(Limit:10000), but List clamps to
// 1000, so a customer with >1000 events in the window reported a truncated
// total_events AND truncated per-meter quantities. The fix reads from
// store.Aggregate (full COUNT(*) + GROUP BY SUM). This test ingests more
// than the clamp and asserts the summary reflects the full set.
func TestGetSummaryServerSideAggregate(t *testing.T) {
	store := newMemStore()
	svc := NewService(store)
	ctx := context.Background()

	const n = listClamp + 250 // 1250 events, over the List clamp
	for i := 0; i < n; i++ {
		if _, err := svc.Ingest(ctx, "t1", IngestInput{
			CustomerID: "cus_big", MeterID: "mtr_tokens", Quantity: dec(1),
		}); err != nil {
			t.Fatalf("ingest %d: %v", i, err)
		}
	}

	from := time.Now().Add(-1 * time.Hour)
	to := time.Now().Add(1 * time.Hour)
	summary, err := svc.GetSummary(ctx, "t1", "cus_big", from, to)
	if err != nil {
		t.Fatalf("GetSummary: %v", err)
	}

	if summary.TotalEvents != n {
		t.Errorf("total_events: got %d, want %d (List-clamped under-count regressed)", summary.TotalEvents, n)
	}
	got := summary.Meters["mtr_tokens"]
	if !got.Equal(dec(int64(n))) {
		t.Errorf("meter total: got %s, want %d (per-meter quantity truncated)", got.String(), n)
	}
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

// P10: quantity 0 meters PRESENCE — the old code silently coerced it to
// 1, so an integrator emitting zero-usage heartbeats was billed one
// unit per event on sum meters, invisible until the invoice.
//
// Mutation-verify: restore the `IsZero → 1` default — this fails.
func TestIngest_ZeroQuantityStaysZero(t *testing.T) {
	svc := NewService(newMemStore())
	e, err := svc.Ingest(context.Background(), "t1", IngestInput{
		CustomerID: "cus_1", MeterID: "mtr_1", // quantity absent = 0
	})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if !e.Quantity.IsZero() {
		t.Errorf("quantity: got %s, want 0 (presence semantics — 1 is the silent-billing bug)", e.Quantity.String())
	}
}

// P10: batch errors carry the row index so the caller can FIX the
// failing event — a bare error string across a 500-event batch was
// undebuggable from the response.
func TestBatchIngest_ErrorsAreIndexed(t *testing.T) {
	svc := NewService(newMemStore())
	ingested, errs := svc.BatchIngest(context.Background(), "t1", []IngestInput{
		{CustomerID: "cus_1", MeterID: "mtr_1", Quantity: dec(1)},
		{CustomerID: "cus_1", MeterID: "mtr_1", Quantity: dec(2)},
		{CustomerID: "", MeterID: "mtr_1", Quantity: dec(3)}, // row 2 (0-based): invalid
	})
	if ingested != 2 {
		t.Fatalf("ingested: got %d, want 2 (partial success)", ingested)
	}
	if len(errs) != 1 {
		t.Fatalf("errors: got %d, want 1", len(errs))
	}
	if !strings.Contains(errs[0].Error(), "event[2]") {
		t.Errorf("batch error not indexed: %q (want event[2] so the caller knows WHICH row)", errs[0].Error())
	}
}
