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
	"github.com/sagarsuperuser/velox/internal/invoice"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
	"github.com/sagarsuperuser/velox/internal/pricing"
	"github.com/sagarsuperuser/velox/internal/subscription"
	"github.com/sagarsuperuser/velox/internal/testutil"
)

// fakeInvoicesSource yields a fixed list of invoices for the integration
// test. Other iterators are no-ops because the integration test seeds
// customers / plans / subscriptions via direct service or store calls
// (much faster than running every prior phase importer just to set up
// the dependency tree).
type fakeInvoicesSource struct {
	invoices []*stripe.Invoice
}

func (f *fakeInvoicesSource) IterateCustomers(ctx context.Context, fn func(*stripe.Customer) error) error {
	return nil
}

func (f *fakeInvoicesSource) IterateProducts(ctx context.Context, fn func(*stripe.Product) error) error {
	return nil
}

func (f *fakeInvoicesSource) IteratePrices(ctx context.Context, fn func(*stripe.Price) error) error {
	return nil
}

func (f *fakeInvoicesSource) IterateSubscriptions(ctx context.Context, fn func(*stripe.Subscription) error) error {
	return nil
}

func (f *fakeInvoicesSource) IterateInvoices(ctx context.Context, fn func(*stripe.Invoice) error) error {
	for _, inv := range f.invoices {
		if err := fn(inv); err != nil {
			return err
		}
	}
	return nil
}

// TestInvoiceImporter_EndToEndPostgres drives the invoice importer against
// a real Postgres database. Three phases — insert, idempotent rerun,
// divergence detection — match the customer / products+prices /
// subscription integration coverage shape so the regression net is
// uniform across all importer phases.
//
// Seeds prerequisite customer + plan + rating-rule + subscription
// directly because Phase 3 depends on those rows existing under the
// matching Stripe ids; running the entire 0/1/2 importer chain just to
// set up four dependency rows would be slow without expanding coverage.
func TestInvoiceImporter_EndToEndPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test; -short skips")
	}

	db := testutil.SetupTestDB(t)
	tenantID := testutil.CreateTestTenant(t, db, "Stripe Importer Phase 3 Invoices")

	customerStore := customer.NewPostgresStore(db)
	customerSvc := customer.NewService(customerStore)
	pricingStore := pricing.NewPostgresStore(db)
	pricingSvc := pricing.NewService(pricingStore)
	subStore := subscription.NewPostgresStore(db)
	invoiceStore := invoice.NewPostgresStore(db)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ctx = postgres.WithLivemode(ctx, false)

	// Seed prerequisite customer.
	cust, err := customerSvc.Create(ctx, tenantID, customer.CreateInput{
		ExternalID:  "cus_int_phase3_001",
		DisplayName: "Phase 3 Customer",
		Email:       "phase3@example.com",
	})
	if err != nil {
		t.Fatalf("seed customer: %v", err)
	}

	// Seed prerequisite plan + rating rule (needed for the subscription).
	plan, err := pricingSvc.CreatePlan(ctx, tenantID, pricing.CreatePlanInput{
		Code:            "prod_int_phase3",
		Name:            "Phase 3 Plan",
		Currency:        "USD",
		BillingInterval: domain.BillingMonthly,
		BaseAmountCents: 4999,
		MeterIDs:        []string{},
	})
	if err != nil {
		t.Fatalf("seed plan: %v", err)
	}
	if _, err := pricingSvc.CreateRatingRule(ctx, tenantID, pricing.CreateRatingRuleInput{
		RuleKey:         "price_int_phase3",
		Name:            "Phase 3 Price",
		Mode:            domain.PricingFlat,
		Currency:        "USD",
		FlatAmountCents: 4999,
	}); err != nil {
		t.Fatalf("seed rating rule: %v", err)
	}

	// Seed prerequisite subscription with code = Stripe sub id (matches
	// what Phase 2's importer produces).
	now := time.Now().UTC()
	periodStart := time.Unix(1701000000, 0).UTC()
	periodEnd := time.Unix(1703678400, 0).UTC()
	sub, err := subStore.Create(ctx, tenantID, domain.Subscription{
		Code:                      "sub_int_phase3_001",
		DisplayName:               "Phase 3 Subscription",
		CustomerID:                cust.ID,
		Status:                    domain.SubscriptionActive,
		BillingTime:               domain.BillingTimeAnniversary,
		StartedAt:                 &now,
		CurrentBillingPeriodStart: &periodStart,
		CurrentBillingPeriodEnd:   &periodEnd,
		Items: []domain.SubscriptionItem{
			{PlanID: plan.ID, Quantity: 1},
		},
	})
	if err != nil {
		t.Fatalf("seed subscription: %v", err)
	}

	// Build a paid Stripe invoice referencing the seeded customer + sub.
	stripeInv := &stripe.Invoice{
		ID:              "in_int_phase3_001",
		Status:          stripe.InvoiceStatusPaid,
		BillingReason:   stripe.InvoiceBillingReasonSubscriptionCycle,
		Currency:        "usd",
		Created:         1701086400,
		PeriodStart:     1701000000,
		PeriodEnd:       1703678400,
		Subtotal:        4999,
		Total:           4999,
		AmountDue:       0,
		AmountPaid:      4999,
		AmountRemaining: 0,
		Number:          "INTEG-0001",
		Customer:        &stripe.Customer{ID: "cus_int_phase3_001"},
		Livemode:        false,
		StatusTransitions: &stripe.InvoiceStatusTransitions{
			FinalizedAt: 1701086400,
			PaidAt:      1701086500,
		},
		Parent: &stripe.InvoiceParent{
			Type: stripe.InvoiceParentTypeSubscriptionDetails,
			SubscriptionDetails: &stripe.InvoiceParentSubscriptionDetails{
				Subscription: &stripe.Subscription{ID: "sub_int_phase3_001"},
			},
		},
		Lines: &stripe.InvoiceLineItemList{
			Data: []*stripe.InvoiceLineItem{
				{
					ID:          "il_int_phase3_001",
					Amount:      4999,
					Currency:    "usd",
					Description: "Phase 3 Plan",
					Quantity:    1,
					Period: &stripe.Period{
						Start: 1701000000,
						End:   1703678400,
					},
				},
			},
		},
	}

	src := &fakeInvoicesSource{invoices: []*stripe.Invoice{stripeInv}}

	runImport := func(t *testing.T) (*importstripe.Report, *bytes.Buffer) {
		t.Helper()
		var buf bytes.Buffer
		report, err := importstripe.NewReport(&buf)
		if err != nil {
			t.Fatalf("NewReport: %v", err)
		}
		imp := &importstripe.InvoiceImporter{
			Source:             src,
			Store:              invoiceStore,
			CustomerLookup:     customerStore,
			SubscriptionLookup: subStore,
			Report:             report,
			TenantID:           tenantID,
			Livemode:           false,
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

	// Verify the invoice landed under the correct customer + subscription
	// and preserved Stripe's verbatim totals + status_transitions.
	persisted, err := invoiceStore.GetByStripeInvoiceID(ctx, tenantID, "in_int_phase3_001")
	if err != nil {
		t.Fatalf("GetByStripeInvoiceID: %v", err)
	}
	if persisted.CustomerID != cust.ID {
		t.Errorf("CustomerID = %q, want %q", persisted.CustomerID, cust.ID)
	}
	if persisted.SubscriptionID != sub.ID {
		t.Errorf("SubscriptionID = %q, want %q", persisted.SubscriptionID, sub.ID)
	}
	if persisted.Status != domain.InvoicePaid {
		t.Errorf("Status = %q, want paid", persisted.Status)
	}
	if persisted.PaymentStatus != domain.PaymentSucceeded {
		t.Errorf("PaymentStatus = %q, want succeeded", persisted.PaymentStatus)
	}
	if persisted.Currency != "USD" {
		t.Errorf("Currency = %q, want USD", persisted.Currency)
	}
	if persisted.SubtotalCents != 4999 {
		t.Errorf("SubtotalCents = %d, want 4999", persisted.SubtotalCents)
	}
	if persisted.TotalAmountCents != 4999 {
		t.Errorf("TotalAmountCents = %d, want 4999", persisted.TotalAmountCents)
	}
	if persisted.AmountPaidCents != 4999 {
		t.Errorf("AmountPaidCents = %d, want 4999", persisted.AmountPaidCents)
	}
	if persisted.PaidAt == nil || persisted.PaidAt.Unix() != 1701086500 {
		t.Errorf("PaidAt = %v, want unix 1701086500", persisted.PaidAt)
	}
	if persisted.IssuedAt == nil || persisted.IssuedAt.Unix() != 1701086400 {
		t.Errorf("IssuedAt = %v, want unix 1701086400", persisted.IssuedAt)
	}
	if persisted.BillingReason != domain.BillingReasonSubscriptionCycle {
		t.Errorf("BillingReason = %q, want subscription_cycle", persisted.BillingReason)
	}
	if persisted.StripeInvoiceID != "in_int_phase3_001" {
		t.Errorf("StripeInvoiceID = %q, want in_int_phase3_001", persisted.StripeInvoiceID)
	}

	// Verify the line item was inserted atomically.
	lines, err := invoiceStore.ListLineItems(ctx, tenantID, persisted.ID)
	if err != nil {
		t.Fatalf("ListLineItems: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("ListLineItems = %d, want 1", len(lines))
	}
	if lines[0].AmountCents != 4999 {
		t.Errorf("line.AmountCents = %d, want 4999", lines[0].AmountCents)
	}
	if lines[0].Description != "Phase 3 Plan" {
		t.Errorf("line.Description = %q, want Phase 3 Plan", lines[0].Description)
	}

	// Phase 2: rerun is idempotent.
	r2, buf2 := runImport(t)
	if r2.SkippedEquiv != 1 {
		t.Errorf("rerun SkippedEquiv = %d, want 1; CSV:\n%s", r2.SkippedEquiv, buf2.String())
	}
	if r2.Inserted != 0 {
		t.Errorf("rerun Inserted = %d, want 0", r2.Inserted)
	}

	// Phase 3: Stripe-side mutation surfaces as divergence; no DB write.
	mutated := *stripeInv
	mutated.Subtotal = 5999
	mutated.Total = 5999
	mutated.AmountPaid = 5999
	src.invoices = []*stripe.Invoice{&mutated}
	r3, buf3 := runImport(t)
	if r3.SkippedDivergent != 1 {
		t.Errorf("third run SkippedDivergent = %d, want 1", r3.SkippedDivergent)
	}
	if !strings.Contains(buf3.String(), "subtotal_cents stripe=5999") {
		t.Errorf("CSV missing subtotal diff; got:\n%s", buf3.String())
	}
	// Confirm the persisted invoice retained the original totals (no
	// overwrite — importer is conservative; operator must reconcile
	// manually).
	check, err := invoiceStore.GetByStripeInvoiceID(ctx, tenantID, "in_int_phase3_001")
	if err != nil {
		t.Fatalf("GetByStripeInvoiceID after divergence: %v", err)
	}
	if check.SubtotalCents != 4999 {
		t.Errorf("SubtotalCents overwritten: got %d, want 4999", check.SubtotalCents)
	}
}
