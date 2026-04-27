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
	"github.com/sagarsuperuser/velox/internal/pricing"
	"github.com/sagarsuperuser/velox/internal/subscription"
	"github.com/sagarsuperuser/velox/internal/testutil"
)

// fakeSubscriptionsSource yields a fixed list of subscriptions for the
// integration test. Other iterators are no-ops because the test seeds
// customers/products/prices via direct service calls (faster than running
// the whole multi-stage importer just to set up the dependency tree).
type fakeSubscriptionsSource struct {
	subs []*stripe.Subscription
}

func (f *fakeSubscriptionsSource) IterateCustomers(ctx context.Context, fn func(*stripe.Customer) error) error {
	return nil
}

func (f *fakeSubscriptionsSource) IterateProducts(ctx context.Context, fn func(*stripe.Product) error) error {
	return nil
}

func (f *fakeSubscriptionsSource) IteratePrices(ctx context.Context, fn func(*stripe.Price) error) error {
	return nil
}

func (f *fakeSubscriptionsSource) IterateSubscriptions(ctx context.Context, fn func(*stripe.Subscription) error) error {
	for _, s := range f.subs {
		if err := fn(s); err != nil {
			return err
		}
	}
	return nil
}

// TestSubscriptionImporter_EndToEndPostgres drives the subscription importer
// against a real Postgres database. Three phases — insert, idempotent
// rerun, divergence detection — match the customer/products/prices
// integration coverage shape.
func TestSubscriptionImporter_EndToEndPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test; -short skips")
	}

	db := testutil.SetupTestDB(t)
	tenantID := testutil.CreateTestTenant(t, db, "Stripe Importer Phase 2 Subscriptions")

	customerStore := customer.NewPostgresStore(db)
	customerSvc := customer.NewService(customerStore)
	pricingStore := pricing.NewPostgresStore(db)
	pricingSvc := pricing.NewService(pricingStore)
	subStore := subscription.NewPostgresStore(db)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ctx = postgres.WithLivemode(ctx, false)

	// Seed prerequisite customer + plan + rating rule directly. Faster than
	// running the full Phase 0/1 importers and the dependency wiring is
	// exactly what the importer relies on.
	cust, err := customerSvc.Create(ctx, tenantID, customer.CreateInput{
		ExternalID:  "cus_int_phase2_001",
		DisplayName: "Phase 2 Customer",
		Email:       "phase2@example.com",
	})
	if err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	plan, err := pricingSvc.CreatePlan(ctx, tenantID, pricing.CreatePlanInput{
		Code:            "prod_int_phase2",
		Name:            "Phase 2 Plan",
		Currency:        "USD",
		BillingInterval: domain.BillingMonthly,
		BaseAmountCents: 4999,
		MeterIDs:        []string{},
	})
	if err != nil {
		t.Fatalf("seed plan: %v", err)
	}
	rule, err := pricingSvc.CreateRatingRule(ctx, tenantID, pricing.CreateRatingRuleInput{
		RuleKey:         "price_int_phase2",
		Name:            "Phase 2 Price",
		Mode:            domain.PricingFlat,
		Currency:        "USD",
		FlatAmountCents: 4999,
	})
	if err != nil {
		t.Fatalf("seed rating rule: %v", err)
	}

	stripeSub := &stripe.Subscription{
		ID:                 "sub_int_phase2_001",
		Customer:           &stripe.Customer{ID: "cus_int_phase2_001"},
		Status:             stripe.SubscriptionStatusActive,
		BillingCycleAnchor: 1701000000,
		Created:            1701000000,
		StartDate:          1701000000,
		Currency:           "usd",
		Items: &stripe.SubscriptionItemList{
			Data: []*stripe.SubscriptionItem{
				{
					ID: "si_int_phase2_001",
					Price: &stripe.Price{
						ID: "price_int_phase2",
						Product: &stripe.Product{
							ID: "prod_int_phase2",
						},
					},
					Quantity:           1,
					CurrentPeriodStart: 1701000000,
					CurrentPeriodEnd:   1703678400,
				},
			},
		},
		Livemode: false,
	}

	src := &fakeSubscriptionsSource{subs: []*stripe.Subscription{stripeSub}}

	runImport := func(t *testing.T) (*importstripe.Report, *bytes.Buffer) {
		t.Helper()
		var buf bytes.Buffer
		report, err := importstripe.NewReport(&buf)
		if err != nil {
			t.Fatalf("NewReport: %v", err)
		}
		imp := &importstripe.SubscriptionImporter{
			Source:         src,
			Store:          subStore,
			CustomerLookup: customerStore,
			RuleLookup:     pricingSvc,
			PlanLookup:     pricingStore,
			Report:         report,
			TenantID:       tenantID,
			Livemode:       false,
		}
		if err := imp.Run(ctx); err != nil {
			t.Fatalf("Run: %v", err)
		}
		_ = report.Close()
		return report, &buf
	}

	// Phase 1: insert.
	r1, buf1 := runImport(t)
	if r1.Inserted != 1 {
		t.Fatalf("first run Inserted = %d, want 1; CSV:\n%s", r1.Inserted, buf1.String())
	}
	if r1.Errored != 0 {
		t.Fatalf("first run Errored = %d, want 0; CSV:\n%s", r1.Errored, buf1.String())
	}

	// Verify the subscription landed under the correct customer + plan and
	// preserved Stripe's verbatim period fields.
	subs, _, err := subStore.List(ctx, subscription.ListFilter{TenantID: tenantID, Limit: 50})
	if err != nil {
		t.Fatalf("list subscriptions: %v", err)
	}
	if len(subs) != 1 {
		t.Fatalf("subs after insert = %d, want 1", len(subs))
	}
	got := subs[0]
	if got.Code != "sub_int_phase2_001" {
		t.Errorf("Code = %q, want sub_int_phase2_001", got.Code)
	}
	if got.CustomerID != cust.ID {
		t.Errorf("CustomerID = %q, want %q", got.CustomerID, cust.ID)
	}
	if got.Status != domain.SubscriptionActive {
		t.Errorf("Status = %q, want active", got.Status)
	}
	if len(got.Items) != 1 {
		t.Fatalf("Items = %d, want 1", len(got.Items))
	}
	if got.Items[0].PlanID != plan.ID {
		t.Errorf("Items[0].PlanID = %q, want %q", got.Items[0].PlanID, plan.ID)
	}
	if got.CurrentBillingPeriodEnd == nil ||
		got.CurrentBillingPeriodEnd.Unix() != 1703678400 {
		t.Errorf("CurrentBillingPeriodEnd = %v, want unix 1703678400",
			got.CurrentBillingPeriodEnd)
	}

	// Phase 2: rerun is idempotent.
	r2, _ := runImport(t)
	if r2.SkippedEquiv != 1 {
		t.Errorf("rerun SkippedEquiv = %d, want 1", r2.SkippedEquiv)
	}
	if r2.Inserted != 0 {
		t.Errorf("rerun Inserted = %d, want 0", r2.Inserted)
	}

	// Phase 3: Stripe-side mutation surfaces as divergence; no DB write.
	mutated := *stripeSub
	mutated.Status = stripe.SubscriptionStatusCanceled
	mutated.CanceledAt = 1702500000
	src.subs = []*stripe.Subscription{&mutated}
	r3, buf3 := runImport(t)
	if r3.SkippedDivergent != 1 {
		t.Errorf("third run SkippedDivergent = %d, want 1", r3.SkippedDivergent)
	}
	if !strings.Contains(buf3.String(), "status stripe=") {
		t.Errorf("CSV missing status diff; got:\n%s", buf3.String())
	}
	// Confirm the persisted sub retained the original status (no overwrite —
	// importer is conservative; operator must reconcile manually).
	subs, _, _ = subStore.List(ctx, subscription.ListFilter{TenantID: tenantID, Limit: 50})
	for _, s := range subs {
		if s.Code == "sub_int_phase2_001" && s.Status != domain.SubscriptionActive {
			t.Errorf("sub status overwritten: got %q, want %q", s.Status, domain.SubscriptionActive)
		}
	}

	// Avoid unused on rule (declared for the seed assertion).
	_ = rule
}
