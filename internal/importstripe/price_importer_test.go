package importstripe

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stripe/stripe-go/v82"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/pricing"
)

// fakePriceSource yields a fixed list of prices in order.
type fakePriceSource struct {
	prices []*stripe.Price
}

func (f *fakePriceSource) IterateCustomers(ctx context.Context, fn func(*stripe.Customer) error) error {
	return nil
}

func (f *fakePriceSource) IterateProducts(ctx context.Context, fn func(*stripe.Product) error) error {
	return nil
}

func (f *fakePriceSource) IteratePrices(ctx context.Context, fn func(*stripe.Price) error) error {
	for _, p := range f.prices {
		if err := fn(p); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakePriceSource) IterateSubscriptions(ctx context.Context, fn func(*stripe.Subscription) error) error {
	return nil
}

func (f *fakePriceSource) IterateInvoices(ctx context.Context, fn func(*stripe.Invoice) error) error {
	return nil
}

func TestPriceImporter_FirstRunInsertsFlatPrice(t *testing.T) {
	store := newFakePlanStore()
	// Seed a plan with the matching code so the price import has somewhere to land.
	plan, _ := store.CreatePlan(context.Background(), "ten_test", pricing.CreatePlanInput{
		Code:            "prod_NfJG2N4m6X",
		Name:            "Premium",
		Currency:        "USD",
		BillingInterval: domain.BillingMonthly,
		BaseAmountCents: 0,
		MeterIDs:        []string{},
	})
	store.createCalls = 0 // reset for assertion clarity

	price := loadPriceFixture(t, "price_flat.json")
	src := &fakePriceSource{prices: []*stripe.Price{price}}
	var buf bytes.Buffer
	report, _ := NewReport(&buf)
	imp := &PriceImporter{
		Source:      src,
		RuleService: store,
		PlanService: store,
		Lookup:      store,
		Report:      report,
		TenantID:    "ten_test",
		Livemode:    true,
	}
	if err := imp.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	_ = report.Close()
	if report.Inserted != 1 {
		t.Errorf("Inserted = %d, want 1", report.Inserted)
	}
	if store.createRules != 1 {
		t.Errorf("createRules = %d, want 1", store.createRules)
	}
	// The plan's base_amount_cents should have been updated from 0 to 4999.
	updated := store.plans[plan.ID]
	if updated.BaseAmountCents != 4999 {
		t.Errorf("plan.BaseAmountCents = %d, want 4999", updated.BaseAmountCents)
	}
	if !strings.Contains(buf.String(), "price_flat001,price,insert") {
		t.Errorf("CSV missing expected insert row; got:\n%s", buf.String())
	}
}

func TestPriceImporter_SecondRunIsSkipEquivalent(t *testing.T) {
	store := newFakePlanStore()
	_, _ = store.CreatePlan(context.Background(), "ten_test", pricing.CreatePlanInput{
		Code: "prod_NfJG2N4m6X", Name: "Premium",
		Currency: "USD", BillingInterval: domain.BillingMonthly, MeterIDs: []string{},
	})
	store.createCalls = 0

	price := loadPriceFixture(t, "price_flat.json")
	src := &fakePriceSource{prices: []*stripe.Price{price}}
	r1, _ := NewReport(&bytes.Buffer{})
	imp := &PriceImporter{Source: src, RuleService: store, PlanService: store, Lookup: store, Report: r1, TenantID: "ten_test", Livemode: true}
	if err := imp.Run(context.Background()); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if r1.Inserted != 1 {
		t.Fatalf("setup: first run should insert 1; got %d", r1.Inserted)
	}
	r2, _ := NewReport(&bytes.Buffer{})
	imp.Report = r2
	if err := imp.Run(context.Background()); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if r2.SkippedEquiv != 1 {
		t.Errorf("SkippedEquiv = %d, want 1", r2.SkippedEquiv)
	}
	if r2.Inserted != 0 {
		t.Errorf("Inserted = %d on rerun, want 0", r2.Inserted)
	}
	if store.createRules != 1 {
		t.Errorf("createRules = %d after rerun, want 1", store.createRules)
	}
}

func TestPriceImporter_StripeChangeIsSkipDivergent(t *testing.T) {
	store := newFakePlanStore()
	_, _ = store.CreatePlan(context.Background(), "ten_test", pricing.CreatePlanInput{
		Code: "prod_NfJG2N4m6X", Name: "Premium",
		Currency: "USD", BillingInterval: domain.BillingMonthly, MeterIDs: []string{},
	})
	store.createCalls = 0

	original := loadPriceFixture(t, "price_flat.json")
	src := &fakePriceSource{prices: []*stripe.Price{original}}
	r1, _ := NewReport(&bytes.Buffer{})
	imp := &PriceImporter{Source: src, RuleService: store, PlanService: store, Lookup: store, Report: r1, TenantID: "ten_test", Livemode: true}
	if err := imp.Run(context.Background()); err != nil {
		t.Fatalf("first Run: %v", err)
	}

	mutated := *original
	mutated.UnitAmount = 5999
	src.prices = []*stripe.Price{&mutated}
	var buf bytes.Buffer
	r2, _ := NewReport(&buf)
	imp.Report = r2
	if err := imp.Run(context.Background()); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	_ = r2.Close()
	if r2.SkippedDivergent != 1 {
		t.Errorf("SkippedDivergent = %d, want 1", r2.SkippedDivergent)
	}
	if !strings.Contains(buf.String(), "flat_amount_cents") {
		t.Errorf("CSV missing flat_amount_cents diff; got:\n%s", buf.String())
	}
}

func TestPriceImporter_MissingPlanErrors(t *testing.T) {
	// No plan seeded — the price's product link won't resolve.
	store := newFakePlanStore()
	price := loadPriceFixture(t, "price_flat.json")
	src := &fakePriceSource{prices: []*stripe.Price{price}}
	var buf bytes.Buffer
	report, _ := NewReport(&buf)
	imp := &PriceImporter{
		Source: src, RuleService: store, PlanService: store, Lookup: store,
		Report: report, TenantID: "ten_test", Livemode: true,
	}
	if err := imp.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	_ = report.Close()
	if report.Errored != 1 {
		t.Errorf("Errored = %d, want 1", report.Errored)
	}
	if !strings.Contains(buf.String(), "run --resource=products first") {
		t.Errorf("CSV missing actionable plan-missing detail; got:\n%s", buf.String())
	}
	if store.createRules != 0 {
		t.Errorf("createRules = %d, want 0 (insert should not have happened)", store.createRules)
	}
}

func TestPriceImporter_DryRunSkipsWrites(t *testing.T) {
	store := newFakePlanStore()
	_, _ = store.CreatePlan(context.Background(), "ten_test", pricing.CreatePlanInput{
		Code: "prod_NfJG2N4m6X", Name: "Premium",
		Currency: "USD", BillingInterval: domain.BillingMonthly, MeterIDs: []string{},
	})
	store.createCalls = 0

	price := loadPriceFixture(t, "price_flat.json")
	src := &fakePriceSource{prices: []*stripe.Price{price}}
	report, _ := NewReport(&bytes.Buffer{})
	imp := &PriceImporter{
		Source: src, RuleService: store, PlanService: store, Lookup: store,
		Report: report, TenantID: "ten_test", Livemode: true, DryRun: true,
	}
	if err := imp.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Inserted != 1 {
		t.Errorf("Inserted (reported) = %d, want 1", report.Inserted)
	}
	if store.createRules != 0 {
		t.Errorf("DryRun: createRules = %d, want 0", store.createRules)
	}
	if store.updateCalls != 0 {
		t.Errorf("DryRun: updateCalls = %d, want 0", store.updateCalls)
	}
}

func TestPriceImporter_TieredPriceErrors(t *testing.T) {
	store := newFakePlanStore()
	_, _ = store.CreatePlan(context.Background(), "ten_test", pricing.CreatePlanInput{
		Code: "prod_NfJG2N4m6X", Name: "Premium",
		Currency: "USD", BillingInterval: domain.BillingMonthly, MeterIDs: []string{},
	})

	price := loadPriceFixture(t, "price_tiered.json")
	src := &fakePriceSource{prices: []*stripe.Price{price}}
	var buf bytes.Buffer
	report, _ := NewReport(&buf)
	imp := &PriceImporter{
		Source: src, RuleService: store, PlanService: store, Lookup: store,
		Report: report, TenantID: "ten_test", Livemode: true,
	}
	if err := imp.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	_ = report.Close()
	if report.Errored != 1 {
		t.Errorf("Errored = %d, want 1", report.Errored)
	}
	if !strings.Contains(buf.String(), "tiered billing_scheme") {
		t.Errorf("CSV missing tiered detail; got:\n%s", buf.String())
	}
}

func TestPriceImporter_LookupByProduct(t *testing.T) {
	// Two plans, only one matches the price's product.id.
	store := newFakePlanStore()
	_, _ = store.CreatePlan(context.Background(), "ten_test", pricing.CreatePlanInput{
		Code: "prod_unrelated", Name: "Other",
		Currency: "USD", BillingInterval: domain.BillingMonthly, MeterIDs: []string{},
	})
	matchPlan, _ := store.CreatePlan(context.Background(), "ten_test", pricing.CreatePlanInput{
		Code: "prod_NfJG2N4m6X", Name: "Premium",
		Currency: "USD", BillingInterval: domain.BillingMonthly, MeterIDs: []string{},
	})

	price := loadPriceFixture(t, "price_flat.json")
	src := &fakePriceSource{prices: []*stripe.Price{price}}
	var buf bytes.Buffer
	report, _ := NewReport(&buf)
	imp := &PriceImporter{
		Source: src, RuleService: store, PlanService: store, Lookup: store,
		Report: report, TenantID: "ten_test", Livemode: true,
	}
	if err := imp.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Inserted != 1 {
		t.Errorf("Inserted = %d, want 1", report.Inserted)
	}
	// Verify the correct plan got patched (not the unrelated one).
	if store.plans[matchPlan.ID].BaseAmountCents != 4999 {
		t.Errorf("matchPlan.BaseAmountCents = %d, want 4999",
			store.plans[matchPlan.ID].BaseAmountCents)
	}
}
