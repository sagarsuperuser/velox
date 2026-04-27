package importstripe_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stripe/stripe-go/v82"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/importstripe"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
	"github.com/sagarsuperuser/velox/internal/pricing"
	"github.com/sagarsuperuser/velox/internal/testutil"
)

// fakeProductsPricesSource yields fixed lists for both products and prices
// in the integration test. Customers iterator is unused.
type fakeProductsPricesSource struct {
	products []*stripe.Product
	prices   []*stripe.Price
}

func (f *fakeProductsPricesSource) IterateCustomers(ctx context.Context, fn func(*stripe.Customer) error) error {
	return nil
}

func (f *fakeProductsPricesSource) IterateProducts(ctx context.Context, fn func(*stripe.Product) error) error {
	for _, p := range f.products {
		if err := fn(p); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeProductsPricesSource) IteratePrices(ctx context.Context, fn func(*stripe.Price) error) error {
	for _, p := range f.prices {
		if err := fn(p); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeProductsPricesSource) IterateSubscriptions(ctx context.Context, fn func(*stripe.Subscription) error) error {
	return nil
}

// TestProductImporter_EndToEndPostgres drives the product importer against
// a real Postgres database to exercise the create / idempotent-rerun /
// divergence-detection flow under RLS + the real validation rules.
func TestProductImporter_EndToEndPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test; -short skips")
	}

	db := testutil.SetupTestDB(t)
	tenantID := testutil.CreateTestTenant(t, db, "Stripe Importer Phase 1 Products")
	store := pricing.NewPostgresStore(db)
	svc := pricing.NewService(store)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ctx = postgres.WithLivemode(ctx, false)

	prod := &stripe.Product{
		ID:          "prod_int_phase1_full",
		Name:        "Integration Premium",
		Description: "End-to-end test product",
		Type:        stripe.ProductTypeService,
		Active:      true,
		Livemode:    false,
	}
	src := &fakeProductsPricesSource{products: []*stripe.Product{prod}}

	runImport := func(t *testing.T, src importstripe.Source) (*importstripe.Report, *bytes.Buffer) {
		t.Helper()
		var buf bytes.Buffer
		report, err := importstripe.NewReport(&buf)
		if err != nil {
			t.Fatalf("NewReport: %v", err)
		}
		imp := &importstripe.ProductImporter{
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

	// Phase 1: insert.
	r1, _ := runImport(t, src)
	if r1.Inserted != 1 {
		t.Fatalf("first run Inserted = %d, want 1", r1.Inserted)
	}
	if r1.Errored != 0 {
		t.Fatalf("first run Errored = %d, want 0", r1.Errored)
	}

	// Verify the plan landed correctly.
	plans, err := store.ListPlans(ctx, tenantID)
	if err != nil {
		t.Fatalf("ListPlans: %v", err)
	}
	var got domain.Plan
	for _, p := range plans {
		if p.Code == prod.ID {
			got = p
			break
		}
	}
	if got.ID == "" {
		t.Fatalf("plan with code %q not found in store; have %d plans", prod.ID, len(plans))
	}
	if got.Name != "Integration Premium" {
		t.Errorf("Name = %q, want Integration Premium", got.Name)
	}
	if got.Currency != "USD" {
		t.Errorf("Currency = %q, want USD (default)", got.Currency)
	}
	if got.BillingInterval != domain.BillingMonthly {
		t.Errorf("BillingInterval = %q, want monthly (default)", got.BillingInterval)
	}

	// Phase 2: rerun is idempotent.
	r2, _ := runImport(t, src)
	if r2.SkippedEquiv != 1 {
		t.Errorf("second run SkippedEquiv = %d, want 1", r2.SkippedEquiv)
	}
	if r2.Inserted != 0 {
		t.Errorf("second run Inserted = %d, want 0", r2.Inserted)
	}

	// Phase 3: Stripe-side mutation is reported as divergence; no DB write.
	mutated := *prod
	mutated.Name = "Integration Premium Plus"
	src.products = []*stripe.Product{&mutated}
	r3, buf3 := runImport(t, src)
	if r3.SkippedDivergent != 1 {
		t.Errorf("third run SkippedDivergent = %d, want 1", r3.SkippedDivergent)
	}
	if !strings.Contains(buf3.String(), "Integration Premium Plus") {
		t.Errorf("CSV missing diff; got:\n%s", buf3.String())
	}
	// Confirm the persisted plan retained the original name (no overwrite
	// in Phase 1 — same conservative policy as customer importer).
	plans, _ = store.ListPlans(ctx, tenantID)
	for _, p := range plans {
		if p.Code == prod.ID && p.Name != "Integration Premium" {
			t.Errorf("plan name overwritten: got %q, want %q", p.Name, "Integration Premium")
		}
	}
}

// TestPriceImporter_EndToEndPostgres drives the price importer against a
// real Postgres database. Verifies that a flat per_unit price (a) creates
// a rating rule and (b) updates the parent plan's base_amount_cents.
func TestPriceImporter_EndToEndPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test; -short skips")
	}

	db := testutil.SetupTestDB(t)
	tenantID := testutil.CreateTestTenant(t, db, "Stripe Importer Phase 1 Prices")
	store := pricing.NewPostgresStore(db)
	svc := pricing.NewService(store)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ctx = postgres.WithLivemode(ctx, false)

	// Seed the parent product first.
	prod := &stripe.Product{
		ID: "prod_int_phase1_for_price", Name: "Integration Plan",
		Type: stripe.ProductTypeService, Active: true, Livemode: false,
	}
	prodSrc := &fakeProductsPricesSource{products: []*stripe.Product{prod}}
	prodReport, _ := importstripe.NewReport(&bytes.Buffer{})
	prodImp := &importstripe.ProductImporter{
		Source: prodSrc, Service: svc, Lookup: store, Report: prodReport,
		TenantID: tenantID, Livemode: false,
	}
	if err := prodImp.Run(ctx); err != nil {
		t.Fatalf("seed product: %v", err)
	}
	_ = prodReport.Close()

	// Now the price.
	price := &stripe.Price{
		ID:            "price_int_phase1_flat",
		Active:        true,
		BillingScheme: stripe.PriceBillingSchemePerUnit,
		Type:          stripe.PriceTypeRecurring,
		Currency:      "usd",
		UnitAmount:    1999,
		Nickname:      "Standard Monthly",
		Product:       prod, // same Stripe product reference
		Recurring: &stripe.PriceRecurring{
			Interval:      stripe.PriceRecurringIntervalMonth,
			IntervalCount: 1,
			UsageType:     stripe.PriceRecurringUsageTypeLicensed,
		},
		Livemode: false,
	}
	priceSrc := &fakeProductsPricesSource{prices: []*stripe.Price{price}}
	var buf bytes.Buffer
	priceReport, _ := importstripe.NewReport(&buf)
	priceImp := &importstripe.PriceImporter{
		Source:      priceSrc,
		RuleService: svc,
		PlanService: svc,
		Lookup:      store,
		Report:      priceReport,
		TenantID:    tenantID,
		Livemode:    false,
	}
	if err := priceImp.Run(ctx); err != nil {
		t.Fatalf("price Run: %v", err)
	}
	_ = priceReport.Close()
	if priceReport.Inserted != 1 {
		t.Fatalf("price Inserted = %d, want 1; CSV:\n%s", priceReport.Inserted, buf.String())
	}

	// Verify the rating rule exists.
	rule, err := svc.GetLatestRuleByKey(ctx, tenantID, price.ID)
	if err != nil {
		t.Fatalf("GetLatestRuleByKey: %v", err)
	}
	if rule.FlatAmountCents != 1999 {
		t.Errorf("rule.FlatAmountCents = %d, want 1999", rule.FlatAmountCents)
	}
	if rule.Mode != domain.PricingFlat {
		t.Errorf("rule.Mode = %q, want flat", rule.Mode)
	}
	if rule.Currency != "USD" {
		t.Errorf("rule.Currency = %q, want USD", rule.Currency)
	}

	// Verify the plan's base_amount_cents was patched.
	plans, _ := store.ListPlans(ctx, tenantID)
	var planID string
	var basePrice int64
	for _, p := range plans {
		if p.Code == prod.ID {
			planID = p.ID
			basePrice = p.BaseAmountCents
			break
		}
	}
	if planID == "" {
		t.Fatal("plan not found post-price-import")
	}
	if basePrice != 1999 {
		t.Errorf("plan.BaseAmountCents = %d, want 1999 (patched by price importer)", basePrice)
	}

	// Rerun is idempotent.
	priceSrc.prices = []*stripe.Price{price}
	r2, _ := importstripe.NewReport(&bytes.Buffer{})
	priceImp.Report = r2
	if err := priceImp.Run(ctx); err != nil {
		t.Fatalf("price rerun: %v", err)
	}
	if r2.SkippedEquiv != 1 {
		t.Errorf("rerun SkippedEquiv = %d, want 1", r2.SkippedEquiv)
	}
}
