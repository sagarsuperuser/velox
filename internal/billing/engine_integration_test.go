package billing_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/billing"
	"github.com/sagarsuperuser/velox/internal/customer"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/invoice"
	"github.com/sagarsuperuser/velox/internal/pricing"
	"github.com/sagarsuperuser/velox/internal/subscription"
	"github.com/sagarsuperuser/velox/internal/tenant"
	"github.com/sagarsuperuser/velox/internal/testutil"
	"github.com/sagarsuperuser/velox/internal/usage"
)

// subStoreAdapter wraps subscription.PostgresStore to implement billing.SubscriptionReader
type subStoreAdapter struct {
	store *subscription.PostgresStore
}

func (a *subStoreAdapter) GetDueBilling(ctx context.Context, before time.Time, limit int) ([]domain.Subscription, error) {
	return a.store.GetDueBilling(ctx, before, limit)
}

func (a *subStoreAdapter) Get(ctx context.Context, tenantID, id string) (domain.Subscription, error) {
	return a.store.Get(ctx, tenantID, id)
}

func (a *subStoreAdapter) UpdateBillingCycle(ctx context.Context, tenantID, id string, start, end, next time.Time) error {
	return a.store.UpdateBillingCycle(ctx, tenantID, id, start, end, next)
}

func (a *subStoreAdapter) ApplyDuePendingItemPlansAtomic(ctx context.Context, tenantID, id string, now time.Time) ([]domain.SubscriptionItem, error) {
	return a.store.ApplyDuePendingItemPlansAtomic(ctx, tenantID, id, now)
}

// pricingStoreAdapter wraps pricing.PostgresStore to implement billing.PricingReader
type pricingStoreAdapter struct {
	store *pricing.PostgresStore
}

func (a *pricingStoreAdapter) GetPlan(ctx context.Context, tenantID, id string) (domain.Plan, error) {
	return a.store.GetPlan(ctx, tenantID, id)
}

func (a *pricingStoreAdapter) GetMeter(ctx context.Context, tenantID, id string) (domain.Meter, error) {
	return a.store.GetMeter(ctx, tenantID, id)
}

func (a *pricingStoreAdapter) GetRatingRule(ctx context.Context, tenantID, id string) (domain.RatingRuleVersion, error) {
	return a.store.GetRatingRule(ctx, tenantID, id)
}

func (a *pricingStoreAdapter) GetLatestRuleByKey(ctx context.Context, tenantID, ruleKey string) (domain.RatingRuleVersion, error) {
	rules, err := a.store.ListRatingRules(ctx, pricing.RatingRuleFilter{
		TenantID:   tenantID,
		RuleKey:    ruleKey,
		LatestOnly: true,
	})
	if err != nil {
		return domain.RatingRuleVersion{}, err
	}
	if len(rules) == 0 {
		return domain.RatingRuleVersion{}, fmt.Errorf("no rule found for key %s", ruleKey)
	}
	return rules[0], nil
}

func (a *pricingStoreAdapter) GetOverride(ctx context.Context, tenantID, customerID, ruleID string) (domain.CustomerPriceOverride, error) {
	return a.store.GetOverride(ctx, tenantID, customerID, ruleID)
}

// usageStoreAdapter wraps usage.PostgresStore to implement billing.UsageAggregator
type usageStoreAdapter struct {
	store *usage.PostgresStore
}

func (a *usageStoreAdapter) AggregateForBillingPeriod(ctx context.Context, tenantID, subID string, meterIDs []string, from, to time.Time) (map[string]int64, error) {
	return a.store.AggregateForBillingPeriod(ctx, tenantID, subID, meterIDs, from, to)
}

func (a *usageStoreAdapter) AggregateForBillingPeriodByAgg(ctx context.Context, tenantID, customerID string, meters map[string]string, from, to time.Time) (map[string]int64, error) {
	return a.store.AggregateForBillingPeriodByAgg(ctx, tenantID, customerID, meters, from, to)
}

// invoiceStoreAdapter wraps invoice.PostgresStore to implement billing.InvoiceWriter
type invoiceStoreAdapter struct {
	store *invoice.PostgresStore
}

func (a *invoiceStoreAdapter) CreateInvoice(ctx context.Context, tenantID string, inv domain.Invoice) (domain.Invoice, error) {
	return a.store.Create(ctx, tenantID, inv)
}

func (a *invoiceStoreAdapter) CreateLineItem(ctx context.Context, tenantID string, item domain.InvoiceLineItem) (domain.InvoiceLineItem, error) {
	return a.store.CreateLineItem(ctx, tenantID, item)
}

func (a *invoiceStoreAdapter) ApplyCreditAmount(ctx context.Context, tenantID, id string, amountCents int64) (domain.Invoice, error) {
	return a.store.ApplyCredits(ctx, tenantID, id, amountCents)
}

func (a *invoiceStoreAdapter) GetInvoice(ctx context.Context, tenantID, id string) (domain.Invoice, error) {
	return a.store.Get(ctx, tenantID, id)
}

func (a *invoiceStoreAdapter) MarkPaid(ctx context.Context, tenantID, id string, stripePaymentIntentID string, paidAt time.Time) (domain.Invoice, error) {
	return a.store.MarkPaid(ctx, tenantID, id, stripePaymentIntentID, paidAt)
}

func (a *invoiceStoreAdapter) CreateInvoiceWithLineItems(ctx context.Context, tenantID string, inv domain.Invoice, items []domain.InvoiceLineItem) (domain.Invoice, error) {
	return a.store.CreateWithLineItems(ctx, tenantID, inv, items)
}

func (a *invoiceStoreAdapter) SetAutoChargePending(ctx context.Context, tenantID, id string, pending bool) error {
	return a.store.SetAutoChargePending(ctx, tenantID, id, pending)
}

func (a *invoiceStoreAdapter) ListAutoChargePending(ctx context.Context, limit int) ([]domain.Invoice, error) {
	return a.store.ListAutoChargePending(ctx, limit)
}

func (a *invoiceStoreAdapter) SetTaxTransaction(ctx context.Context, tenantID, id string, taxTransactionID string) error {
	return a.store.SetTaxTransaction(ctx, tenantID, id, taxTransactionID)
}

func (a *invoiceStoreAdapter) ListLineItems(ctx context.Context, tenantID, invoiceID string) ([]domain.InvoiceLineItem, error) {
	return a.store.ListLineItems(ctx, tenantID, invoiceID)
}

func (a *invoiceStoreAdapter) ApplyDiscountAtomic(ctx context.Context, tenantID, invoiceID string, update domain.InvoiceDiscountUpdate, lineItems []domain.InvoiceLineItem) (domain.Invoice, error) {
	return a.store.ApplyDiscountAtomic(ctx, tenantID, invoiceID, update, lineItems)
}

// TestFullBillingCycle_E2E tests the complete flow against real Postgres:
// tenant → customer → meter → rating rule → plan → subscription → usage → billing engine → invoice
func TestFullBillingCycle_E2E(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx := context.Background()

	// Stores
	customerStore := customer.NewPostgresStore(db)
	pricingStore := pricing.NewPostgresStore(db)
	subStore := subscription.NewPostgresStore(db)
	usageStore := usage.NewPostgresStore(db)
	invoiceStore := invoice.NewPostgresStore(db)

	// 1. Create tenant
	tenantID := testutil.CreateTestTenant(t, db, "Billing Test Corp")

	// 2. Create customer
	cust, err := customerStore.Create(ctx, tenantID, domain.Customer{
		ExternalID: "cus_billing_test", DisplayName: "Test Customer",
	})
	if err != nil {
		t.Fatalf("create customer: %v", err)
	}

	// 3. Create rating rules
	apiRule, err := pricingStore.CreateRatingRule(ctx, tenantID, domain.RatingRuleVersion{
		RuleKey: "api_calls", Name: "API Calls Pricing", Version: 1,
		LifecycleState: domain.RatingRuleActive, Mode: domain.PricingGraduated,
		Currency: "USD",
		GraduatedTiers: []domain.RatingTier{
			{UpTo: 1000, UnitAmountCents: 10}, // $0.10 per call up to 1000
			{UpTo: 0, UnitAmountCents: 5},     // $0.05 per call after 1000
		},
	})
	if err != nil {
		t.Fatalf("create api rule: %v", err)
	}

	storageRule, err := pricingStore.CreateRatingRule(ctx, tenantID, domain.RatingRuleVersion{
		RuleKey: "storage_gb", Name: "Storage Pricing", Version: 1,
		LifecycleState: domain.RatingRuleActive, Mode: domain.PricingFlat,
		Currency: "USD", FlatAmountCents: 2500, // $25 flat
	})
	if err != nil {
		t.Fatalf("create storage rule: %v", err)
	}

	// 4. Create meters
	apiMeter, err := pricingStore.CreateMeter(ctx, tenantID, domain.Meter{
		Key: "api_calls", Name: "API Calls", Unit: "calls",
		Aggregation: "sum", RatingRuleVersionID: apiRule.ID,
	})
	if err != nil {
		t.Fatalf("create api meter: %v", err)
	}

	storageMeter, err := pricingStore.CreateMeter(ctx, tenantID, domain.Meter{
		Key: "storage_gb", Name: "Storage", Unit: "GB",
		Aggregation: "sum", RatingRuleVersionID: storageRule.ID,
	})
	if err != nil {
		t.Fatalf("create storage meter: %v", err)
	}

	// 5. Create plan
	plan, err := pricingStore.CreatePlan(ctx, tenantID, domain.Plan{
		Code: "pro", Name: "Pro Plan", Currency: "USD",
		BillingInterval: domain.BillingMonthly, Status: domain.PlanActive,
		BaseAmountCents: 4900, // $49 base
		MeterIDs:        []string{apiMeter.ID, storageMeter.ID},
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}

	// 6. Create subscription with billing period set
	periodStart := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	nextBilling := periodEnd

	sub, err := subStore.Create(ctx, tenantID, domain.Subscription{
		Code: "sub-e2e-001", DisplayName: "Pro Monthly",
		CustomerID: cust.ID,
		Items:      []domain.SubscriptionItem{{PlanID: plan.ID, Quantity: 1}},
		Status:     domain.SubscriptionActive, BillingTime: domain.BillingTimeCalendar,
		StartedAt: &periodStart,
	})
	if err != nil {
		t.Fatalf("create subscription: %v", err)
	}

	// Set billing period
	if err := subStore.UpdateBillingCycle(ctx, tenantID, sub.ID, periodStart, periodEnd, nextBilling); err != nil {
		t.Fatalf("set billing cycle: %v", err)
	}

	// 7. Ingest usage events
	for i := 0; i < 5; i++ {
		ts := periodStart.Add(time.Duration(i) * 24 * time.Hour)
		_, err := usageStore.Ingest(ctx, tenantID, domain.UsageEvent{
			CustomerID: cust.ID, MeterID: apiMeter.ID, SubscriptionID: sub.ID,
			Quantity: 300, Timestamp: ts,
		})
		if err != nil {
			t.Fatalf("ingest api usage %d: %v", i, err)
		}
	}
	// Total API calls: 5 * 300 = 1500

	_, err = usageStore.Ingest(ctx, tenantID, domain.UsageEvent{
		CustomerID: cust.ID, MeterID: storageMeter.ID, SubscriptionID: sub.ID,
		Quantity: 50, Timestamp: periodStart.Add(48 * time.Hour),
	})
	if err != nil {
		t.Fatalf("ingest storage usage: %v", err)
	}
	// Total storage: 50 GB

	// 8. Run billing engine with a real settings store (exercises the
	// UPSERT path in NextInvoiceNumber for a tenant with no prior settings row).
	settingsStore := tenant.NewSettingsStore(db)
	engine := billing.NewEngine(
		&subStoreAdapter{subStore},
		&usageStoreAdapter{usageStore},
		&pricingStoreAdapter{pricingStore},
		&invoiceStoreAdapter{invoiceStore},
		nil, settingsStore, nil, nil, nil,
	)

	count, errs := engine.RunCycle(ctx, 50)
	if len(errs) > 0 {
		t.Fatalf("billing cycle errors: %v", errs)
	}
	if count != 1 {
		t.Fatalf("expected 1 invoice, got %d", count)
	}

	// 9. Verify invoice
	invoices, total, err := invoiceStore.List(ctx, invoice.ListFilter{TenantID: tenantID})
	if err != nil {
		t.Fatalf("list invoices: %v", err)
	}
	if total != 1 {
		t.Fatalf("expected 1 invoice, got %d", total)
	}

	inv := invoices[0]
	if inv.CustomerID != cust.ID {
		t.Errorf("customer_id: got %q, want %q", inv.CustomerID, cust.ID)
	}
	if inv.SubscriptionID != sub.ID {
		t.Errorf("subscription_id: got %q, want %q", inv.SubscriptionID, sub.ID)
	}
	if inv.Status != domain.InvoiceFinalized {
		t.Errorf("status: got %q, want finalized", inv.Status)
	}

	// Expected totals:
	// Base fee: $49.00 = 4900 cents
	// API: graduated — 1000 × $0.10 + 500 × $0.05 = 12500 cents = $125.00
	// Storage: flat $25/unit × 50 GB = 125000 cents = $1250.00
	// Total: 4900 + 12500 + 125000 = 142400 cents = $1424.00
	expectedTotal := int64(4900 + 12500 + 125000)
	if inv.TotalAmountCents != expectedTotal {
		t.Errorf("total: got %d cents ($%.2f), want %d cents ($%.2f)",
			inv.TotalAmountCents, float64(inv.TotalAmountCents)/100,
			expectedTotal, float64(expectedTotal)/100)
	}

	// 10. Verify line items
	items, err := invoiceStore.ListLineItems(ctx, tenantID, inv.ID)
	if err != nil {
		t.Fatalf("list line items: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 line items (base + 2 usage), got %d", len(items))
	}

	for _, item := range items {
		switch item.LineType {
		case domain.LineTypeBaseFee:
			if item.AmountCents != 4900 {
				t.Errorf("base fee: got %d, want 4900", item.AmountCents)
			}
		case domain.LineTypeUsage:
			if item.MeterID == apiMeter.ID {
				if item.Quantity != 1500 {
					t.Errorf("API quantity: got %d, want 1500", item.Quantity)
				}
				if item.AmountCents != 12500 {
					t.Errorf("API amount: got %d, want 12500", item.AmountCents)
				}
			}
			if item.MeterID == storageMeter.ID {
				if item.AmountCents != 125000 {
					t.Errorf("storage amount: got %d, want 125000", item.AmountCents)
				}
			}
		}
	}

	// 11. Verify billing cycle advanced
	updatedSub, err := subStore.Get(ctx, tenantID, sub.ID)
	if err != nil {
		t.Fatalf("get updated sub: %v", err)
	}
	if updatedSub.CurrentBillingPeriodStart == nil || updatedSub.CurrentBillingPeriodStart.Month() != time.April {
		t.Errorf("next period should start April, got %v", updatedSub.CurrentBillingPeriodStart)
	}
	if updatedSub.CurrentBillingPeriodEnd == nil || updatedSub.CurrentBillingPeriodEnd.Month() != time.May {
		t.Errorf("next period should end May, got %v", updatedSub.CurrentBillingPeriodEnd)
	}

	t.Logf("E2E billing cycle passed: Invoice %s, Total: $%.2f (%d line items)",
		inv.InvoiceNumber, float64(inv.TotalAmountCents)/100, len(items))
}
