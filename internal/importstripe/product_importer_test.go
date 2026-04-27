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

// fakeProductSource yields a fixed list of products in order.
type fakeProductSource struct {
	products []*stripe.Product
}

func (f *fakeProductSource) IterateCustomers(ctx context.Context, fn func(*stripe.Customer) error) error {
	return nil
}

func (f *fakeProductSource) IterateProducts(ctx context.Context, fn func(*stripe.Product) error) error {
	for _, p := range f.products {
		if err := fn(p); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeProductSource) IteratePrices(ctx context.Context, fn func(*stripe.Price) error) error {
	return nil
}

func (f *fakeProductSource) IterateSubscriptions(ctx context.Context, fn func(*stripe.Subscription) error) error {
	return nil
}

func (f *fakeProductSource) IterateInvoices(ctx context.Context, fn func(*stripe.Invoice) error) error {
	return nil
}

// fakePlanStore is the minimal PlanService + PlanLookup stand-in used by
// the unit-level driver tests. It models plans by Velox id and supports
// list-by-tenant; ListPlans returns deterministic order.
type fakePlanStore struct {
	plans       map[string]domain.Plan
	createCalls int
	updateCalls int
	rules       map[string]domain.RatingRuleVersion
	createRules int
}

func newFakePlanStore() *fakePlanStore {
	return &fakePlanStore{
		plans: map[string]domain.Plan{},
		rules: map[string]domain.RatingRuleVersion{},
	}
}

func (s *fakePlanStore) CreatePlan(ctx context.Context, tenantID string, in pricing.CreatePlanInput) (domain.Plan, error) {
	s.createCalls++
	id := "vlx_pln_" + in.Code
	plan := domain.Plan{
		ID:              id,
		TenantID:        tenantID,
		Code:            in.Code,
		Name:            in.Name,
		Description:     in.Description,
		Currency:        in.Currency,
		BillingInterval: in.BillingInterval,
		Status:          domain.PlanActive,
		BaseAmountCents: in.BaseAmountCents,
		MeterIDs:        in.MeterIDs,
		TaxCode:         in.TaxCode,
	}
	s.plans[id] = plan
	return plan, nil
}

func (s *fakePlanStore) UpdatePlan(ctx context.Context, tenantID, id string, in pricing.CreatePlanInput) (domain.Plan, error) {
	s.updateCalls++
	plan, ok := s.plans[id]
	if !ok {
		return domain.Plan{}, errPlanNotFoundFake
	}
	if in.Name != "" {
		plan.Name = in.Name
	}
	plan.Description = in.Description
	if in.BaseAmountCents > 0 {
		plan.BaseAmountCents = in.BaseAmountCents
	}
	if in.MeterIDs != nil {
		plan.MeterIDs = in.MeterIDs
	}
	if in.Status != "" {
		plan.Status = domain.PlanStatus(in.Status)
	}
	plan.TaxCode = in.TaxCode
	s.plans[id] = plan
	return plan, nil
}

func (s *fakePlanStore) ListPlans(ctx context.Context, tenantID string) ([]domain.Plan, error) {
	out := make([]domain.Plan, 0, len(s.plans))
	for _, p := range s.plans {
		out = append(out, p)
	}
	return out, nil
}

func (s *fakePlanStore) CreateRatingRule(ctx context.Context, tenantID string, in pricing.CreateRatingRuleInput) (domain.RatingRuleVersion, error) {
	s.createRules++
	id := "vlx_rrv_" + in.RuleKey
	rule := domain.RatingRuleVersion{
		ID:              id,
		TenantID:        tenantID,
		RuleKey:         in.RuleKey,
		Name:            in.Name,
		Version:         1,
		LifecycleState:  domain.RatingRuleActive,
		Mode:            in.Mode,
		Currency:        strings.ToUpper(in.Currency),
		FlatAmountCents: in.FlatAmountCents,
	}
	s.rules[in.RuleKey] = rule
	return rule, nil
}

func (s *fakePlanStore) GetLatestRuleByKey(ctx context.Context, tenantID, ruleKey string) (domain.RatingRuleVersion, error) {
	rule, ok := s.rules[ruleKey]
	if !ok {
		return domain.RatingRuleVersion{}, errRuleNotFoundFake
	}
	return rule, nil
}

// errPlanNotFoundFake / errRuleNotFoundFake are local sentinels so tests
// don't need to import errs. The price_importer treats GetLatestRuleByKey
// errors generically as "not found", so any error works.
var (
	errPlanNotFoundFake = newFakeErr("plan not found")
	errRuleNotFoundFake = newFakeErr("rating rule not found")
)

type fakeErr string

func (e fakeErr) Error() string { return string(e) }
func newFakeErr(s string) error { return fakeErr(s) }

func TestProductImporter_FirstRunInsertsAll(t *testing.T) {
	store := newFakePlanStore()
	src := &fakeProductSource{products: []*stripe.Product{
		loadProductFixture(t, "product_full.json"),
		loadProductFixture(t, "product_minimal.json"),
	}}
	// product_minimal has livemode=false, switch to that to keep it simple.
	// Or run with a loop matching each product's livemode. Use livemode=true,
	// rerun the second product through livemode=false separately.
	src.products = []*stripe.Product{loadProductFixture(t, "product_full.json")}

	var buf bytes.Buffer
	report, err := NewReport(&buf)
	if err != nil {
		t.Fatalf("NewReport: %v", err)
	}
	imp := &ProductImporter{
		Source: src, Service: store, Lookup: store, Report: report,
		TenantID: "ten_test", Livemode: true,
	}
	if err := imp.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	_ = report.Close()
	if report.Inserted != 1 {
		t.Errorf("Inserted = %d, want 1", report.Inserted)
	}
	if store.createCalls != 1 {
		t.Errorf("createCalls = %d, want 1", store.createCalls)
	}
	if !strings.Contains(buf.String(), "prod_NfJG2N4m6X,product,insert") {
		t.Errorf("CSV missing expected insert row; got:\n%s", buf.String())
	}
}

func TestProductImporter_SecondRunIsSkipEquivalent(t *testing.T) {
	store := newFakePlanStore()
	src := &fakeProductSource{products: []*stripe.Product{loadProductFixture(t, "product_full.json")}}
	r1, _ := NewReport(&bytes.Buffer{})
	imp := &ProductImporter{Source: src, Service: store, Lookup: store, Report: r1, TenantID: "ten_test", Livemode: true}
	if err := imp.Run(context.Background()); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if r1.Inserted != 1 {
		t.Fatalf("setup: first run should insert 1; got %d", r1.Inserted)
	}
	var buf2 bytes.Buffer
	r2, _ := NewReport(&buf2)
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
	if store.createCalls != 1 {
		t.Errorf("createCalls = %d after rerun, want 1", store.createCalls)
	}
}

func TestProductImporter_StripeChangeIsSkipDivergent(t *testing.T) {
	store := newFakePlanStore()
	original := loadProductFixture(t, "product_full.json")
	src := &fakeProductSource{products: []*stripe.Product{original}}
	r1, _ := NewReport(&bytes.Buffer{})
	imp := &ProductImporter{Source: src, Service: store, Lookup: store, Report: r1, TenantID: "ten_test", Livemode: true}
	if err := imp.Run(context.Background()); err != nil {
		t.Fatalf("first Run: %v", err)
	}

	// Stripe-side mutation: name changed.
	mutated := *original
	mutated.Name = "Premium Plus"
	src.products = []*stripe.Product{&mutated}
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
	if !strings.Contains(buf.String(), "name stripe=") || !strings.Contains(buf.String(), "Premium Plus") {
		t.Errorf("CSV missing name diff; got:\n%s", buf.String())
	}
}

func TestProductImporter_DryRunSkipsWrites(t *testing.T) {
	store := newFakePlanStore()
	src := &fakeProductSource{products: []*stripe.Product{loadProductFixture(t, "product_full.json")}}
	report, _ := NewReport(&bytes.Buffer{})
	imp := &ProductImporter{
		Source: src, Service: store, Lookup: store, Report: report,
		TenantID: "ten_test", Livemode: true, DryRun: true,
	}
	if err := imp.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Inserted != 1 {
		t.Errorf("Inserted (reported) = %d, want 1", report.Inserted)
	}
	if store.createCalls != 0 {
		t.Errorf("DryRun: createCalls = %d, want 0", store.createCalls)
	}
}

func TestProductImporter_LivemodeMismatchErrors(t *testing.T) {
	store := newFakePlanStore()
	prod := loadProductFixture(t, "product_full.json") // livemode=true
	src := &fakeProductSource{products: []*stripe.Product{prod}}
	var buf bytes.Buffer
	report, _ := NewReport(&buf)
	imp := &ProductImporter{
		Source: src, Service: store, Lookup: store, Report: report,
		TenantID: "ten_test", Livemode: false,
	}
	if err := imp.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	_ = report.Close()
	if report.Errored != 1 {
		t.Errorf("Errored = %d, want 1", report.Errored)
	}
	if !strings.Contains(buf.String(), "livemode mismatch") {
		t.Errorf("CSV missing livemode mismatch detail; got:\n%s", buf.String())
	}
	if store.createCalls != 0 {
		t.Errorf("livemode mismatch: createCalls = %d, want 0", store.createCalls)
	}
}

func TestProductImporter_EmptyIDIsError(t *testing.T) {
	store := newFakePlanStore()
	src := &fakeProductSource{products: []*stripe.Product{{ID: "", Livemode: true}}}
	var buf bytes.Buffer
	report, _ := NewReport(&buf)
	imp := &ProductImporter{Source: src, Service: store, Lookup: store, Report: report, TenantID: "ten_test", Livemode: true}
	if err := imp.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Errored != 1 {
		t.Errorf("Errored = %d, want 1", report.Errored)
	}
}
