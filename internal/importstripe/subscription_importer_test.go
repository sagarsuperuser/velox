package importstripe

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stripe/stripe-go/v82"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/subscription"
)

// fakeSubscriptionSource yields a fixed list of subscriptions in order.
type fakeSubscriptionSource struct {
	subs []*stripe.Subscription
}

func (f *fakeSubscriptionSource) IterateCustomers(ctx context.Context, fn func(*stripe.Customer) error) error {
	return nil
}

func (f *fakeSubscriptionSource) IterateProducts(ctx context.Context, fn func(*stripe.Product) error) error {
	return nil
}

func (f *fakeSubscriptionSource) IteratePrices(ctx context.Context, fn func(*stripe.Price) error) error {
	return nil
}

func (f *fakeSubscriptionSource) IterateSubscriptions(ctx context.Context, fn func(*stripe.Subscription) error) error {
	for _, s := range f.subs {
		if err := fn(s); err != nil {
			return err
		}
	}
	return nil
}

// fakeSubscriptionStore is the minimal SubscriptionStore stand-in used by
// the unit-level driver tests. Models subs by Velox id with a secondary
// index from code → id for lookup.
type fakeSubscriptionStore struct {
	subs          map[string]domain.Subscription
	byCode        map[string]string
	createCalls   int
	scheduleCalls int
}

func newFakeSubscriptionStore() *fakeSubscriptionStore {
	return &fakeSubscriptionStore{
		subs:   map[string]domain.Subscription{},
		byCode: map[string]string{},
	}
}

func (s *fakeSubscriptionStore) Create(ctx context.Context, tenantID string, sub domain.Subscription) (domain.Subscription, error) {
	s.createCalls++
	id := "vlx_sub_" + sub.Code
	sub.ID = id
	sub.TenantID = tenantID
	sub.CreatedAt = time.Now().UTC()
	sub.UpdatedAt = sub.CreatedAt
	// Hydrate items with synthetic IDs so the round-trip looks like the
	// real store.
	for i := range sub.Items {
		sub.Items[i].ID = "vlx_si_" + sub.Code + "_" + sub.Items[i].PlanID
		sub.Items[i].SubscriptionID = id
		sub.Items[i].TenantID = tenantID
	}
	s.subs[id] = sub
	s.byCode[sub.Code] = id
	return sub, nil
}

func (s *fakeSubscriptionStore) ScheduleCancellation(ctx context.Context, tenantID, id string, cancelAt *time.Time, cancelAtPeriodEnd bool) (domain.Subscription, error) {
	s.scheduleCalls++
	sub, ok := s.subs[id]
	if !ok {
		return domain.Subscription{}, errs.ErrNotFound
	}
	sub.CancelAt = cancelAt
	sub.CancelAtPeriodEnd = cancelAtPeriodEnd
	s.subs[id] = sub
	return sub, nil
}

func (s *fakeSubscriptionStore) List(ctx context.Context, filter subscription.ListFilter) ([]domain.Subscription, int, error) {
	out := make([]domain.Subscription, 0, len(s.subs))
	for _, sub := range s.subs {
		out = append(out, sub)
	}
	return out, len(out), nil
}

// fakeCustomerByExternal is a minimal SubscriptionCustomerLookup. Pre-seeded
// in each test with the customers the sub expects.
type fakeCustomerByExternal struct {
	byExternal map[string]domain.Customer
}

func (f *fakeCustomerByExternal) GetByExternalID(ctx context.Context, tenantID, externalID string) (domain.Customer, error) {
	c, ok := f.byExternal[externalID]
	if !ok {
		return domain.Customer{}, errs.ErrNotFound
	}
	return c, nil
}

// fakeRuleByKey is a minimal SubscriptionRuleLookup.
type fakeRuleByKey struct {
	byKey map[string]domain.RatingRuleVersion
}

func (f *fakeRuleByKey) GetLatestRuleByKey(ctx context.Context, tenantID, ruleKey string) (domain.RatingRuleVersion, error) {
	r, ok := f.byKey[ruleKey]
	if !ok {
		return domain.RatingRuleVersion{}, errRuleNotFoundFake
	}
	return r, nil
}

// seedDeps wires the three lookups for a test in one place.
func seedDeps() (*fakeSubscriptionStore, *fakeCustomerByExternal, *fakeRuleByKey, *fakePlanStore) {
	store := newFakeSubscriptionStore()
	customers := &fakeCustomerByExternal{byExternal: map[string]domain.Customer{
		"cus_NfJG2N4m6X": {ID: "vlx_cus_NfJG2N4m6X", ExternalID: "cus_NfJG2N4m6X"},
		"cus_trial_001":  {ID: "vlx_cus_trial_001", ExternalID: "cus_trial_001"},
	}}
	rules := &fakeRuleByKey{byKey: map[string]domain.RatingRuleVersion{
		"price_flat001": {ID: "vlx_rrv_price_flat001", RuleKey: "price_flat001"},
	}}
	plans := newFakePlanStore()
	plans.plans["vlx_pln_prod_NfJG2N4m6X"] = domain.Plan{
		ID: "vlx_pln_prod_NfJG2N4m6X", Code: "prod_NfJG2N4m6X",
		Name: "Premium", Currency: "USD", BillingInterval: domain.BillingMonthly,
	}
	return store, customers, rules, plans
}

func TestSubscriptionImporter_FirstRunInsertsActive(t *testing.T) {
	store, customers, rules, plans := seedDeps()
	src := &fakeSubscriptionSource{subs: []*stripe.Subscription{
		loadSubscriptionFixture(t, "subscription_full.json"),
	}}
	var buf bytes.Buffer
	report, _ := NewReport(&buf)
	imp := &SubscriptionImporter{
		Source:         src,
		Store:          store,
		CustomerLookup: customers,
		RuleLookup:     rules,
		PlanLookup:     plans,
		Report:         report,
		TenantID:       "ten_test",
		Livemode:       true,
	}
	if err := imp.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	_ = report.Close()
	if report.Inserted != 1 {
		t.Errorf("Inserted = %d, want 1; CSV:\n%s", report.Inserted, buf.String())
	}
	if store.createCalls != 1 {
		t.Errorf("createCalls = %d, want 1", store.createCalls)
	}
	if store.scheduleCalls != 0 {
		t.Errorf("scheduleCalls = %d, want 0 (no cancel schedule on full active sub)", store.scheduleCalls)
	}
	// Verify the customer / plan ids were resolved correctly.
	persisted := store.subs[store.byCode["sub_NfJG2N4m6X"]]
	if persisted.CustomerID != "vlx_cus_NfJG2N4m6X" {
		t.Errorf("CustomerID = %q, want vlx_cus_NfJG2N4m6X", persisted.CustomerID)
	}
	if len(persisted.Items) != 1 || persisted.Items[0].PlanID != "vlx_pln_prod_NfJG2N4m6X" {
		t.Errorf("Items = %+v, want [{PlanID: vlx_pln_prod_NfJG2N4m6X}]", persisted.Items)
	}
	if !strings.Contains(buf.String(), "sub_NfJG2N4m6X,subscription,insert") {
		t.Errorf("CSV missing expected insert row; got:\n%s", buf.String())
	}
}

func TestSubscriptionImporter_SecondRunIsSkipEquivalent(t *testing.T) {
	store, customers, rules, plans := seedDeps()
	src := &fakeSubscriptionSource{subs: []*stripe.Subscription{
		loadSubscriptionFixture(t, "subscription_full.json"),
	}}
	r1, _ := NewReport(&bytes.Buffer{})
	imp := &SubscriptionImporter{
		Source: src, Store: store, CustomerLookup: customers, RuleLookup: rules,
		PlanLookup: plans, Report: r1, TenantID: "ten_test", Livemode: true,
	}
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
	if store.createCalls != 1 {
		t.Errorf("createCalls = %d after rerun, want 1", store.createCalls)
	}
}

func TestSubscriptionImporter_StripeChangeIsSkipDivergent(t *testing.T) {
	store, customers, rules, plans := seedDeps()
	original := loadSubscriptionFixture(t, "subscription_full.json")
	src := &fakeSubscriptionSource{subs: []*stripe.Subscription{original}}
	r1, _ := NewReport(&bytes.Buffer{})
	imp := &SubscriptionImporter{
		Source: src, Store: store, CustomerLookup: customers, RuleLookup: rules,
		PlanLookup: plans, Report: r1, TenantID: "ten_test", Livemode: true,
	}
	if err := imp.Run(context.Background()); err != nil {
		t.Fatalf("first Run: %v", err)
	}

	// Stripe-side mutation: status changed from active to canceled.
	mutated := *original
	mutated.Status = stripe.SubscriptionStatusCanceled
	mutated.CanceledAt = 1702500000
	src.subs = []*stripe.Subscription{&mutated}
	var buf bytes.Buffer
	r2, _ := NewReport(&buf)
	imp.Report = r2
	if err := imp.Run(context.Background()); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	_ = r2.Close()
	if r2.SkippedDivergent != 1 {
		t.Errorf("SkippedDivergent = %d, want 1; CSV:\n%s", r2.SkippedDivergent, buf.String())
	}
	if !strings.Contains(buf.String(), "status stripe=") {
		t.Errorf("CSV missing status diff; got:\n%s", buf.String())
	}
}

func TestSubscriptionImporter_DryRunSkipsWrites(t *testing.T) {
	store, customers, rules, plans := seedDeps()
	src := &fakeSubscriptionSource{subs: []*stripe.Subscription{
		loadSubscriptionFixture(t, "subscription_full.json"),
	}}
	report, _ := NewReport(&bytes.Buffer{})
	imp := &SubscriptionImporter{
		Source: src, Store: store, CustomerLookup: customers, RuleLookup: rules,
		PlanLookup: plans, Report: report, TenantID: "ten_test", Livemode: true,
		DryRun: true,
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

func TestSubscriptionImporter_LivemodeMismatchErrors(t *testing.T) {
	store, customers, rules, plans := seedDeps()
	src := &fakeSubscriptionSource{subs: []*stripe.Subscription{
		loadSubscriptionFixture(t, "subscription_full.json"), // livemode=true
	}}
	var buf bytes.Buffer
	report, _ := NewReport(&buf)
	imp := &SubscriptionImporter{
		Source: src, Store: store, CustomerLookup: customers, RuleLookup: rules,
		PlanLookup: plans, Report: report, TenantID: "ten_test", Livemode: false,
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
		t.Errorf("createCalls = %d, want 0 on livemode mismatch", store.createCalls)
	}
}

func TestSubscriptionImporter_MissingCustomerErrors(t *testing.T) {
	store := newFakeSubscriptionStore()
	customers := &fakeCustomerByExternal{byExternal: map[string]domain.Customer{}} // empty
	rules := &fakeRuleByKey{byKey: map[string]domain.RatingRuleVersion{
		"price_flat001": {ID: "vlx_rrv_price_flat001", RuleKey: "price_flat001"},
	}}
	plans := newFakePlanStore()
	plans.plans["vlx_pln_prod_NfJG2N4m6X"] = domain.Plan{
		ID: "vlx_pln_prod_NfJG2N4m6X", Code: "prod_NfJG2N4m6X",
	}

	src := &fakeSubscriptionSource{subs: []*stripe.Subscription{
		loadSubscriptionFixture(t, "subscription_full.json"),
	}}
	var buf bytes.Buffer
	report, _ := NewReport(&buf)
	imp := &SubscriptionImporter{
		Source: src, Store: store, CustomerLookup: customers, RuleLookup: rules,
		PlanLookup: plans, Report: report, TenantID: "ten_test", Livemode: true,
	}
	if err := imp.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	_ = report.Close()
	if report.Errored != 1 {
		t.Errorf("Errored = %d, want 1", report.Errored)
	}
	if !strings.Contains(buf.String(), "run --resource=customers first") {
		t.Errorf("CSV missing actionable customer-missing detail; got:\n%s", buf.String())
	}
}

func TestSubscriptionImporter_MissingPriceRuleErrors(t *testing.T) {
	store := newFakeSubscriptionStore()
	customers := &fakeCustomerByExternal{byExternal: map[string]domain.Customer{
		"cus_NfJG2N4m6X": {ID: "vlx_cus_NfJG2N4m6X", ExternalID: "cus_NfJG2N4m6X"},
	}}
	rules := &fakeRuleByKey{byKey: map[string]domain.RatingRuleVersion{}} // empty — no price imported
	plans := newFakePlanStore()
	plans.plans["vlx_pln_prod_NfJG2N4m6X"] = domain.Plan{
		ID: "vlx_pln_prod_NfJG2N4m6X", Code: "prod_NfJG2N4m6X",
	}

	src := &fakeSubscriptionSource{subs: []*stripe.Subscription{
		loadSubscriptionFixture(t, "subscription_full.json"),
	}}
	var buf bytes.Buffer
	report, _ := NewReport(&buf)
	imp := &SubscriptionImporter{
		Source: src, Store: store, CustomerLookup: customers, RuleLookup: rules,
		PlanLookup: plans, Report: report, TenantID: "ten_test", Livemode: true,
	}
	if err := imp.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	_ = report.Close()
	if report.Errored != 1 {
		t.Errorf("Errored = %d, want 1", report.Errored)
	}
	if !strings.Contains(buf.String(), "run --resource=prices first") {
		t.Errorf("CSV missing actionable price-missing detail; got:\n%s", buf.String())
	}
}

func TestSubscriptionImporter_CancelAtPeriodEndPatchesViaSchedule(t *testing.T) {
	store, customers, rules, plans := seedDeps()
	src := &fakeSubscriptionSource{subs: []*stripe.Subscription{
		loadSubscriptionFixture(t, "subscription_cancel_at_period_end.json"),
	}}
	report, _ := NewReport(&bytes.Buffer{})
	imp := &SubscriptionImporter{
		Source: src, Store: store, CustomerLookup: customers, RuleLookup: rules,
		PlanLookup: plans, Report: report, TenantID: "ten_test", Livemode: true,
	}
	if err := imp.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Inserted != 1 {
		t.Errorf("Inserted = %d, want 1", report.Inserted)
	}
	if store.scheduleCalls != 1 {
		t.Errorf("scheduleCalls = %d, want 1 (cancel_at_period_end set)", store.scheduleCalls)
	}
	persisted := store.subs[store.byCode["sub_cape_001"]]
	if !persisted.CancelAtPeriodEnd {
		t.Error("persisted CancelAtPeriodEnd = false, want true")
	}
}

func TestSubscriptionImporter_MultiItemErrors(t *testing.T) {
	store, customers, rules, plans := seedDeps()
	src := &fakeSubscriptionSource{subs: []*stripe.Subscription{
		loadSubscriptionFixture(t, "subscription_multi_item.json"),
	}}
	var buf bytes.Buffer
	report, _ := NewReport(&buf)
	imp := &SubscriptionImporter{
		Source: src, Store: store, CustomerLookup: customers, RuleLookup: rules,
		PlanLookup: plans, Report: report, TenantID: "ten_test", Livemode: true,
	}
	if err := imp.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	_ = report.Close()
	if report.Errored != 1 {
		t.Errorf("Errored = %d, want 1", report.Errored)
	}
	if !strings.Contains(buf.String(), "multiple items") {
		t.Errorf("CSV missing multi-item detail; got:\n%s", buf.String())
	}
	if store.createCalls != 0 {
		t.Errorf("createCalls = %d, want 0 on multi-item rejection", store.createCalls)
	}
}
