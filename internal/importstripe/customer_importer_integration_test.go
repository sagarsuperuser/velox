package importstripe_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stripe/stripe-go/v82"

	"github.com/sagarsuperuser/velox/internal/customer"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/importstripe"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
	"github.com/sagarsuperuser/velox/internal/testutil"
)

// fakeIntegrationSource yields a fixed list to drive the importer in
// integration tests without hitting the Stripe API.
type fakeIntegrationSource struct {
	customers []*stripe.Customer
}

func (f *fakeIntegrationSource) IterateCustomers(ctx context.Context, fn func(*stripe.Customer) error) error {
	for _, c := range f.customers {
		if err := fn(c); err != nil {
			return err
		}
	}
	return nil
}

// IterateProducts / IteratePrices are no-op for the customer integration
// test. The Source interface widened in Phase 1.
func (f *fakeIntegrationSource) IterateProducts(ctx context.Context, fn func(*stripe.Product) error) error {
	return nil
}

func (f *fakeIntegrationSource) IteratePrices(ctx context.Context, fn func(*stripe.Price) error) error {
	return nil
}

// TestCustomerImporter_EndToEndPostgres drives the importer against a real
// Postgres database. Single test, three phases — keeps RLS/encryption setup
// cost amortised across all the assertions we care about (insert, idempotent
// rerun, divergence detection).
func TestCustomerImporter_EndToEndPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test; -short skips")
	}

	db := testutil.SetupTestDB(t)
	tenantID := testutil.CreateTestTenant(t, db, "Stripe Importer Phase 0")
	store := customer.NewPostgresStore(db)
	svc := customer.NewService(store)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Importer runs in test mode (livemode=false). RLS reads app.livemode
	// from session; the importer wires it via WithLivemode in main, but
	// CustomerImporter.Run itself doesn't, so we set it here.
	ctx = postgres.WithLivemode(ctx, false)

	cust := &stripe.Customer{
		ID:       "cus_int_phase0_full",
		Name:     "Integration Co",
		Email:    "int@example.com",
		Phone:    "+1-415-555-0199",
		Currency: stripe.Currency("usd"),
		Address: &stripe.Address{
			Line1:      "1 Integration Way",
			City:       "San Francisco",
			State:      "CA",
			PostalCode: "94105",
			Country:    "US",
		},
		TaxExempt: stripe.CustomerTaxExemptNone,
		TaxIDs: &stripe.TaxIDList{
			Data: []*stripe.TaxID{{
				Type:    stripe.TaxIDType("eu_vat"),
				Value:   "DE999",
				Country: "DE",
			}},
		},
		Livemode: false,
	}

	src := &fakeIntegrationSource{customers: []*stripe.Customer{cust}}

	runImport := func(t *testing.T, src importstripe.Source) (*importstripe.Report, *bytes.Buffer) {
		t.Helper()
		var buf bytes.Buffer
		report, err := importstripe.NewReport(&buf)
		if err != nil {
			t.Fatalf("NewReport: %v", err)
		}
		imp := &importstripe.CustomerImporter{
			Source:   src,
			Service:  svc,
			Lookup:   store,
			Report:   report,
			TenantID: tenantID,
			Livemode: false,
		}
		if err := imp.Run(ctx); err != nil {
			t.Fatalf("Run: %v", err)
		}
		_ = report.Close()
		return report, &buf
	}

	// Phase 1: first run inserts.
	r1, _ := runImport(t, src)
	if r1.Inserted != 1 {
		t.Fatalf("first run Inserted = %d, want 1", r1.Inserted)
	}
	if r1.Errored != 0 {
		t.Fatalf("first run Errored = %d, want 0", r1.Errored)
	}

	// Verify the customer landed correctly under RLS.
	got, err := store.GetByExternalID(ctx, tenantID, cust.ID)
	if err != nil {
		t.Fatalf("GetByExternalID: %v", err)
	}
	if got.DisplayName != "Integration Co" {
		t.Errorf("DisplayName = %q, want Integration Co", got.DisplayName)
	}
	if got.Email != "int@example.com" {
		t.Errorf("Email = %q, want int@example.com", got.Email)
	}
	bp, err := store.GetBillingProfile(ctx, tenantID, got.ID)
	if err != nil {
		t.Fatalf("GetBillingProfile: %v", err)
	}
	if bp.Country != "US" {
		t.Errorf("Country = %q, want US", bp.Country)
	}
	if bp.Currency != "USD" {
		t.Errorf("Currency = %q, want USD", bp.Currency)
	}
	if bp.TaxID != "DE999" {
		t.Errorf("TaxID = %q, want DE999", bp.TaxID)
	}
	if bp.ProfileStatus != domain.BillingProfileReady {
		t.Errorf("ProfileStatus = %q, want ready", bp.ProfileStatus)
	}

	// Phase 2: second run with identical input is fully idempotent.
	r2, _ := runImport(t, src)
	if r2.Inserted != 0 {
		t.Errorf("second run Inserted = %d, want 0", r2.Inserted)
	}
	if r2.SkippedEquiv != 1 {
		t.Errorf("second run SkippedEquiv = %d, want 1", r2.SkippedEquiv)
	}

	// Phase 3: a Stripe-side change shows up as skip-divergent in the report,
	// no DB write happens (Phase 0 doesn't overwrite).
	mutated := *cust
	mutated.Email = "int-changed@example.com"
	src.customers = []*stripe.Customer{&mutated}
	r3, buf3 := runImport(t, src)
	if r3.SkippedDivergent != 1 {
		t.Errorf("third run SkippedDivergent = %d, want 1", r3.SkippedDivergent)
	}
	if !strings.Contains(buf3.String(), "email stripe=") || !strings.Contains(buf3.String(), "int-changed@example.com") {
		t.Errorf("CSV missing email diff; got:\n%s", buf3.String())
	}
	persisted, err := store.GetByExternalID(ctx, tenantID, cust.ID)
	if err != nil {
		t.Fatalf("post-divergent GetByExternalID: %v", err)
	}
	if persisted.Email != "int@example.com" {
		t.Errorf("Phase 0 must NOT overwrite on divergence; Email = %q", persisted.Email)
	}
}

// TestCustomerImporter_DryRunDoesNotPersist confirms --dry-run produces a
// report identical in shape to a real run, but writes no rows.
func TestCustomerImporter_DryRunDoesNotPersist(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test; -short skips")
	}

	db := testutil.SetupTestDB(t)
	tenantID := testutil.CreateTestTenant(t, db, "Stripe Importer DryRun")
	store := customer.NewPostgresStore(db)
	svc := customer.NewService(store)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ctx = postgres.WithLivemode(ctx, false)

	src := &fakeIntegrationSource{customers: []*stripe.Customer{
		{ID: "cus_dry_001", Name: "Dry One", Email: "dry@example.com", Livemode: false},
	}}

	var buf bytes.Buffer
	report, err := importstripe.NewReport(&buf)
	if err != nil {
		t.Fatalf("NewReport: %v", err)
	}
	imp := &importstripe.CustomerImporter{
		Source: src, Service: svc, Lookup: store, Report: report,
		TenantID: tenantID, Livemode: false, DryRun: true,
	}
	if err := imp.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	_ = report.Close()
	if report.Inserted != 1 {
		t.Errorf("Inserted (reported) = %d, want 1", report.Inserted)
	}
	// Confirm nothing actually landed in the DB.
	if _, err := store.GetByExternalID(ctx, tenantID, "cus_dry_001"); err == nil {
		t.Error("DryRun: customer was persisted despite --dry-run")
	}
}
