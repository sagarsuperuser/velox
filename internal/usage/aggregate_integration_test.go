package usage_test

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/sagarsuperuser/velox/internal/testutil"
	"github.com/sagarsuperuser/velox/internal/usage"
)

// TestAggregateRespectsListFilters is the wire-shape regression test for
// GET /v1/usage-events/aggregate. It exists so the dashboard's stat cards
// + "Usage by Meter" breakdown can never silently regress to reducing
// over the current page of paginated events (the bug reported in #7).
//
// Each subtest fixes one piece of the contract:
//
//   - Filters: customer_id, meter_id, and from/to all narrow the scope
//     in lockstep with the equivalent List query.
//   - Decimal precision: SUM over NUMERIC(38,12) round-trips losslessly.
//     0.5 + 0.5 + 0.0001 must surface as "1.0001" — losing the trailing
//     digit would mean the dashboard's Total Units misses sub-cent
//     usage and the operator can't trust it.
//   - by_meter ordering: per-meter rows sort DESC by total so the
//     dashboard's horizontal-bar breakdown renders in priority order.
//   - by_meter is non-nil on empty result so the JSON encoder emits
//     "by_meter": [] rather than "by_meter": null and the React side
//     can iterate without a null-check.
func TestAggregateRespectsListFilters(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: skipped in -short mode")
	}
	db := testutil.SetupTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store := usage.NewPostgresStore(db)
	svc := usage.NewService(store)

	t.Run("totals + by_meter respect customer + meter + window filters", func(t *testing.T) {
		tenantID := testutil.CreateTestTenant(t, db, "Agg Filters")
		custA := insertTestCustomer(t, db, tenantID, "cus_a")
		custB := insertTestCustomer(t, db, tenantID, "cus_b")
		meterX := insertTestMeter(t, db, tenantID, "mtr_x", "tokens_x")
		meterY := insertTestMeter(t, db, tenantID, "mtr_y", "tokens_y")

		// Window = [now-1h, now+1h). One out-of-window event per
		// customer/meter so the window filter is exercised, not just
		// implicit-passing.
		now := time.Now().UTC()
		winFrom := now.Add(-1 * time.Hour)
		winTo := now.Add(1 * time.Hour)
		outOfWindow := now.Add(-3 * time.Hour)

		// In-window: A/X = 10+5; A/Y = 7; B/X = 99 (different customer).
		ingestAt(t, ctx, store, tenantID, custA, meterX, decimal.NewFromInt(10), nil, now)
		ingestAt(t, ctx, store, tenantID, custA, meterX, decimal.NewFromInt(5), nil, now)
		ingestAt(t, ctx, store, tenantID, custA, meterY, decimal.NewFromInt(7), nil, now)
		ingestAt(t, ctx, store, tenantID, custB, meterX, decimal.NewFromInt(99), nil, now)

		// Out-of-window: must not count against any of the three
		// scoped queries below.
		ingestAt(t, ctx, store, tenantID, custA, meterX, decimal.NewFromInt(1000), nil, outOfWindow)

		// Customer A inside the window: total events 3, total units 22,
		// 2 meters, 1 customer; by_meter sorted desc by total.
		got, err := svc.Aggregate(ctx, usage.ListFilter{
			TenantID: tenantID, CustomerID: custA, From: &winFrom, To: &winTo,
		})
		if err != nil {
			t.Fatalf("aggregate scoped to A: %v", err)
		}
		if got.TotalEvents != 3 {
			t.Errorf("total_events: got %d, want 3", got.TotalEvents)
		}
		if !got.TotalUnits.Equal(decimal.NewFromInt(22)) {
			t.Errorf("total_units: got %s, want 22", got.TotalUnits.String())
		}
		if got.ActiveMeters != 2 {
			t.Errorf("active_meters: got %d, want 2", got.ActiveMeters)
		}
		if got.ActiveCustomers != 1 {
			t.Errorf("active_customers: got %d, want 1", got.ActiveCustomers)
		}
		if len(got.ByMeter) != 2 {
			t.Fatalf("by_meter rows: got %d, want 2", len(got.ByMeter))
		}
		if got.ByMeter[0].MeterID != meterX || !got.ByMeter[0].Total.Equal(decimal.NewFromInt(15)) {
			t.Errorf("by_meter[0]: got (%s, %s), want (%s, 15)",
				got.ByMeter[0].MeterID, got.ByMeter[0].Total.String(), meterX)
		}
		if got.ByMeter[1].MeterID != meterY || !got.ByMeter[1].Total.Equal(decimal.NewFromInt(7)) {
			t.Errorf("by_meter[1]: got (%s, %s), want (%s, 7)",
				got.ByMeter[1].MeterID, got.ByMeter[1].Total.String(), meterY)
		}

		// Meter X inside the window across both customers: total events 3,
		// total units 114 (10+5+99), 1 meter, 2 customers.
		gotX, err := svc.Aggregate(ctx, usage.ListFilter{
			TenantID: tenantID, MeterID: meterX, From: &winFrom, To: &winTo,
		})
		if err != nil {
			t.Fatalf("aggregate scoped to X: %v", err)
		}
		if gotX.TotalEvents != 3 || !gotX.TotalUnits.Equal(decimal.NewFromInt(114)) ||
			gotX.ActiveMeters != 1 || gotX.ActiveCustomers != 2 {
			t.Errorf("scope=meterX: got %+v", gotX)
		}

		// No filters but the window: total events 4 (out-of-window event
		// excluded), total units 121, 2 meters, 2 customers.
		gotAll, err := svc.Aggregate(ctx, usage.ListFilter{
			TenantID: tenantID, From: &winFrom, To: &winTo,
		})
		if err != nil {
			t.Fatalf("aggregate full window: %v", err)
		}
		if gotAll.TotalEvents != 4 || !gotAll.TotalUnits.Equal(decimal.NewFromInt(121)) ||
			gotAll.ActiveMeters != 2 || gotAll.ActiveCustomers != 2 {
			t.Errorf("scope=window: got %+v", gotAll)
		}
	})

	t.Run("decimal precision: 0.5 + 0.5 + 0.0001 = 1.0001", func(t *testing.T) {
		tenantID := testutil.CreateTestTenant(t, db, "Agg Decimal")
		cust := insertTestCustomer(t, db, tenantID, "cus_dec")
		meter := insertTestMeter(t, db, tenantID, "mtr_dec", "tokens_dec")

		half, _ := decimal.NewFromString("0.5")
		small, _ := decimal.NewFromString("0.0001")
		want, _ := decimal.NewFromString("1.0001")

		ingest(t, ctx, store, tenantID, cust, meter, half, nil)
		ingest(t, ctx, store, tenantID, cust, meter, half, nil)
		ingest(t, ctx, store, tenantID, cust, meter, small, nil)

		got, err := svc.Aggregate(ctx, usage.ListFilter{TenantID: tenantID})
		if err != nil {
			t.Fatalf("aggregate: %v", err)
		}
		if !got.TotalUnits.Equal(want) {
			t.Errorf("total_units: got %s, want %s", got.TotalUnits.String(), want.String())
		}
		if len(got.ByMeter) != 1 || !got.ByMeter[0].Total.Equal(want) {
			t.Errorf("by_meter: got %+v, want one row with total %s", got.ByMeter, want.String())
		}
	})

	t.Run("empty filter result: by_meter is [], not null", func(t *testing.T) {
		tenantID := testutil.CreateTestTenant(t, db, "Agg Empty")
		// No events ingested.
		got, err := svc.Aggregate(ctx, usage.ListFilter{TenantID: tenantID})
		if err != nil {
			t.Fatalf("aggregate: %v", err)
		}
		if got.TotalEvents != 0 {
			t.Errorf("total_events: got %d, want 0", got.TotalEvents)
		}
		if !got.TotalUnits.IsZero() {
			t.Errorf("total_units: got %s, want 0", got.TotalUnits.String())
		}
		if got.ByMeter == nil {
			t.Error("by_meter must be a non-nil slice so JSON encodes [], not null")
		}
		if len(got.ByMeter) != 0 {
			t.Errorf("by_meter: got %d rows, want 0", len(got.ByMeter))
		}
	})

	t.Run("cross-tenant isolation", func(t *testing.T) {
		tenantA := testutil.CreateTestTenant(t, db, "Agg Tenant A")
		tenantB := testutil.CreateTestTenant(t, db, "Agg Tenant B")
		custA := insertTestCustomer(t, db, tenantA, "cus_iso_a")
		meterA := insertTestMeter(t, db, tenantA, "mtr_iso_a", "tokens_iso_a")
		ingest(t, ctx, store, tenantA, custA, meterA, decimal.NewFromInt(42), nil)

		// Tenant B asks for the same shape — RLS scopes the query to
		// its own (empty) row set.
		got, err := svc.Aggregate(ctx, usage.ListFilter{TenantID: tenantB})
		if err != nil {
			t.Fatalf("aggregate tenant B: %v", err)
		}
		if got.TotalEvents != 0 {
			t.Errorf("RLS leak: tenant B sees %d events from tenant A", got.TotalEvents)
		}
	})
}
