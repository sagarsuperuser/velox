package customer_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/sagarsuperuser/velox/internal/customer"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
	"github.com/sagarsuperuser/velox/internal/testutil"
	"github.com/sagarsuperuser/velox/internal/usage"
)

// TestGDPR_ExportCustomerData_MultiDimUsageEvents asserts that the GDPR
// right-to-portability export carries every usage_events row owned by the
// data subject, including the per-event `dimensions` JSONB payload that
// multi-dim meters use to dispatch pricing rules at finalize time.
//
// Why this matters: GDPR Art. 20 requires "the personal data concerning
// him or her" — and on a multi-dim meter the dimensions ARE the data
// subject's record (which model, which region, which call shape). A
// dimension-stripped export would let the operator reissue a "GDPR-
// compliant" file that is impossible to reconcile against the original
// invoices, which is the exact reconciliation right the regulation
// codifies.
//
// If a future change ever drops `Dimensions` from the export struct,
// changes the JSON wire name, or stops fetching usage events at all,
// this test fires before the regression ships.
func TestGDPR_ExportCustomerData_MultiDimUsageEvents(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	tenantID := testutil.CreateTestTenant(t, db, "GDPR Multi-Dim")
	customerStore := customer.NewPostgresStore(db)

	cust, err := customerStore.Create(ctx, tenantID, domain.Customer{
		ExternalID:  "cus_gdpr_multidim",
		DisplayName: "AI Workload Inc",
		Email:       "ops@aiworkload.example",
		Status:      domain.CustomerStatusActive,
	})
	if err != nil {
		t.Fatalf("create customer: %v", err)
	}

	meterID := insertGDPRTestMeter(t, db, tenantID, "AI Tokens", "ai_tokens")

	// Two events with non-empty Dimensions — the canonical AI-pricing
	// shape: model + region. Mixing string and bool values exercises
	// the JSONB scalar contract validateDimensions enforces.
	wantDimsA := map[string]any{
		"region": "us-east-1",
		"model":  "claude-opus",
		"cached": false,
	}
	wantDimsB := map[string]any{
		"region": "eu-west-1",
		"model":  "claude-sonnet",
		"cached": true,
	}

	usageSvc := usage.NewService(usage.NewPostgresStore(db))
	if _, err := usageSvc.Ingest(ctx, tenantID, usage.IngestInput{
		CustomerID: cust.ID,
		MeterID:    meterID,
		Quantity:   decimal.NewFromInt(1500),
		Dimensions: wantDimsA,
	}); err != nil {
		t.Fatalf("ingest event A: %v", err)
	}
	if _, err := usageSvc.Ingest(ctx, tenantID, usage.IngestInput{
		CustomerID: cust.ID,
		MeterID:    meterID,
		Quantity:   decimal.NewFromInt(750),
		Dimensions: wantDimsB,
	}); err != nil {
		t.Fatalf("ingest event B: %v", err)
	}

	export, err := newGDPRServiceForTest(db).ExportCustomerData(ctx, tenantID, cust.ID)
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	if export.UsageEventsTruncated {
		t.Errorf("export marked truncated for 2 events — only fires above %d", customer.MaxExportedUsageEvents)
	}
	if len(export.UsageEvents) != 2 {
		t.Fatalf("expected 2 usage events, got %d", len(export.UsageEvents))
	}

	// Map by quantity (deterministic per-event identifier here) so the
	// assertion is order-independent — store.List sorts by timestamp DESC
	// and two ingests within the same goroutine can land in either order
	// when the resolution drops below microsecond.
	byQty := map[string]domain.UsageEvent{}
	for _, e := range export.UsageEvents {
		byQty[e.Quantity.String()] = e
	}
	gotA, ok := byQty["1500"]
	if !ok {
		t.Fatalf("event with qty=1500 missing from export; got %+v", export.UsageEvents)
	}
	gotB, ok := byQty["750"]
	if !ok {
		t.Fatalf("event with qty=750 missing from export; got %+v", export.UsageEvents)
	}

	// Exact-match the dimensions payload. Postgres JSONB normalizes bool
	// and string scalars on round-trip, so DeepEqual against the input
	// map is the right contract: any silent coercion (bool→string) here
	// is a regression.
	if !reflect.DeepEqual(gotA.Dimensions, wantDimsA) {
		t.Errorf("event A dimensions mismatch:\n got  %#v\n want %#v", gotA.Dimensions, wantDimsA)
	}
	if !reflect.DeepEqual(gotB.Dimensions, wantDimsB) {
		t.Errorf("event B dimensions mismatch:\n got  %#v\n want %#v", gotB.Dimensions, wantDimsB)
	}

	// Sanity-check the rest of the row also round-trips. If meter_id or
	// customer_id were lost the export would still pass the dimension
	// assertion but be useless for reconciliation, so guard against
	// partial-row regressions.
	for _, e := range []domain.UsageEvent{gotA, gotB} {
		if e.CustomerID != cust.ID {
			t.Errorf("usage event customer_id: got %q, want %q", e.CustomerID, cust.ID)
		}
		if e.MeterID != meterID {
			t.Errorf("usage event meter_id: got %q, want %q", e.MeterID, meterID)
		}
		if e.Timestamp.IsZero() {
			t.Errorf("usage event timestamp not populated")
		}
	}
}

// insertGDPRTestMeter creates a meter directly via SQL because the
// pricing.Store does not yet expose a meter constructor at the surface
// the GDPR test needs. Mirrors the helper in usage/backfill_integration_test.go
// (kept local because Go doesn't expose helpers across _test.go packages).
func insertGDPRTestMeter(t *testing.T, db *postgres.DB, tenantID, name, key string) string {
	t.Helper()
	id := postgres.NewID("vlx_mtr")
	tx, err := db.BeginTx(context.Background(), postgres.TxTenant, tenantID)
	if err != nil {
		t.Fatalf("begin meter: %v", err)
	}
	defer postgres.Rollback(tx)

	_, err = tx.ExecContext(context.Background(), `
		INSERT INTO meters (id, tenant_id, name, key, unit, aggregation)
		VALUES ($1, $2, $3, $4, 'requests', 'sum')
	`, id, tenantID, name, key)
	if err != nil {
		t.Fatalf("insert meter: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit meter: %v", err)
	}
	return id
}
