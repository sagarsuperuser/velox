package tax_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/platform/postgres"
	"github.com/sagarsuperuser/velox/internal/tax"
	"github.com/sagarsuperuser/velox/internal/testutil"
)

// TestPostgresStore_Record inserts one row via the convenience Record method
// and verifies the payload round-trips through the JSONB columns. Uses
// Record (not RecordCalculation) because that is the surface the billing
// engine depends on — a broken signature here would silently drop audit rows.
func TestPostgresStore_Record(t *testing.T) {
	db := testutil.SetupTestDB(t)
	tenantID := testutil.CreateTestTenant(t, db, "Tax Store Record")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	store := tax.NewPostgresStore(db)

	req := tax.Request{
		Currency: "USD",
		LineItems: []tax.RequestLine{
			{Ref: "line_0", AmountCents: 10000, TaxCode: "txcd_10103001"},
		},
	}
	res := &tax.Result{
		Provider:        "stripe_tax",
		CalculationID:   "calc_test_abc",
		TotalTaxCents:   725,
		EffectiveRateBP: 725,
		TaxName:         "Sales Tax",
		Lines: []tax.ResultLine{{
			Ref:            "line_0",
			NetAmountCents: 10000,
			TaxAmountCents: 725,
			TaxRateBP:      725,
			Jurisdiction:   "US-CA",
		}},
	}

	// Empty invoice_id matches the draft-time call pattern in the billing
	// engine — the calculation is recorded before the invoice row exists.
	id, err := store.Record(ctx, tenantID, "", req, res)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if !strings.HasPrefix(id, "vlx_tcalc_") {
		t.Errorf("id %q does not look like a tax_calculation id (expected vlx_tcalc_ prefix)", id)
	}

	// Read back via TxBypass so we verify the write landed regardless of RLS.
	tx, err := db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		t.Fatalf("begin read tx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	var (
		gotTenant, gotInvoice, gotProvider, gotRef string
		reqJSON, resJSON                           []byte
	)
	err = tx.QueryRowContext(ctx, `
		SELECT tenant_id, COALESCE(invoice_id,''), provider, provider_ref, request, response
		FROM tax_calculations WHERE id = $1
	`, id).Scan(&gotTenant, &gotInvoice, &gotProvider, &gotRef, &reqJSON, &resJSON)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if gotTenant != tenantID {
		t.Errorf("tenant_id = %q, want %q", gotTenant, tenantID)
	}
	if gotInvoice != "" {
		t.Errorf("invoice_id = %q, want empty (draft-time calc)", gotInvoice)
	}
	if gotProvider != "stripe_tax" {
		t.Errorf("provider = %q, want stripe_tax", gotProvider)
	}
	if gotRef != "calc_test_abc" {
		t.Errorf("provider_ref = %q, want calc_test_abc", gotRef)
	}

	// Request round-trips through JSON — the Stripe calc ID and jurisdiction
	// must survive so future audit queries can reconstruct the decision.
	var parsed map[string]any
	if err := json.Unmarshal(resJSON, &parsed); err != nil {
		t.Fatalf("response JSON malformed: %v", err)
	}
	if parsed["CalculationID"] != "calc_test_abc" {
		t.Errorf("response.CalculationID = %v, want calc_test_abc", parsed["CalculationID"])
	}
}

// TestPostgresStore_Record_TenantIsolation verifies RLS prevents cross-tenant
// reads of tax_calculations — critical because calculation payloads can
// include customer addresses, tax IDs, and jurisdiction detail other tenants
// must never see.
func TestPostgresStore_Record_TenantIsolation(t *testing.T) {
	db := testutil.SetupTestDB(t)
	tenantA := testutil.CreateTestTenant(t, db, "Tenant A")
	tenantB := testutil.CreateTestTenant(t, db, "Tenant B")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	store := tax.NewPostgresStore(db)
	req := tax.Request{LineItems: []tax.RequestLine{{Ref: "line_0", AmountCents: 1000}}}
	res := &tax.Result{Provider: "manual", TotalTaxCents: 100}

	idA, err := store.Record(ctx, tenantA, "", req, res)
	if err != nil {
		t.Fatalf("Record tenantA: %v", err)
	}

	// Tenant B scoped tx must not see tenant A's row.
	tx, err := db.BeginTx(ctx, postgres.TxTenant, tenantB)
	if err != nil {
		t.Fatalf("begin tenantB tx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	var count int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tax_calculations WHERE id = $1`, idA,
	).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("tenant B sees tenant A's calculation row — RLS not enforced")
	}
}
