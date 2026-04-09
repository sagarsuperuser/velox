package billing

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// ---------------------------------------------------------------------------
// In-memory implementations of the billing engine's interfaces
// ---------------------------------------------------------------------------

type mockSubs struct {
	subs          map[string]domain.Subscription
	cycleUpdated  map[string]bool
}

func (m *mockSubs) GetDueBilling(_ context.Context, before time.Time, limit int) ([]domain.Subscription, error) {
	var result []domain.Subscription
	for _, s := range m.subs {
		if s.Status == domain.SubscriptionActive && s.NextBillingAt != nil && !s.NextBillingAt.After(before) {
			result = append(result, s)
		}
	}
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (m *mockSubs) Get(_ context.Context, _, id string) (domain.Subscription, error) {
	s, ok := m.subs[id]
	if !ok {
		return domain.Subscription{}, fmt.Errorf("not found")
	}
	return s, nil
}

func (m *mockSubs) UpdateBillingCycle(_ context.Context, _, id string, start, end, next time.Time) error {
	s, ok := m.subs[id]
	if !ok {
		return fmt.Errorf("not found")
	}
	s.CurrentBillingPeriodStart = &start
	s.CurrentBillingPeriodEnd = &end
	s.NextBillingAt = &next
	m.subs[id] = s
	m.cycleUpdated[id] = true
	return nil
}

type mockUsage struct {
	totals map[string]int64 // meterID -> quantity
}

func (m *mockUsage) AggregateForBillingPeriod(_ context.Context, _, _ string, meterIDs []string, _, _ time.Time) (map[string]int64, error) {
	result := make(map[string]int64)
	for _, id := range meterIDs {
		if qty, ok := m.totals[id]; ok {
			result[id] = qty
		}
	}
	return result, nil
}

type mockPricing struct {
	plans     map[string]domain.Plan
	meters    map[string]domain.Meter
	rules     map[string]domain.RatingRuleVersion
	overrides map[string]domain.CustomerPriceOverride // key: customerID+ruleID
}

func (m *mockPricing) GetPlan(_ context.Context, _, id string) (domain.Plan, error) {
	p, ok := m.plans[id]
	if !ok {
		return domain.Plan{}, fmt.Errorf("plan not found")
	}
	return p, nil
}

func (m *mockPricing) GetMeter(_ context.Context, _, id string) (domain.Meter, error) {
	meter, ok := m.meters[id]
	if !ok {
		return domain.Meter{}, fmt.Errorf("meter not found")
	}
	return meter, nil
}

func (m *mockPricing) GetRatingRule(_ context.Context, _, id string) (domain.RatingRuleVersion, error) {
	r, ok := m.rules[id]
	if !ok {
		return domain.RatingRuleVersion{}, fmt.Errorf("rule not found")
	}
	return r, nil
}

func (m *mockPricing) GetOverride(_ context.Context, _, customerID, ruleID string) (domain.CustomerPriceOverride, error) {
	if m.overrides == nil {
		return domain.CustomerPriceOverride{}, fmt.Errorf("not found")
	}
	o, ok := m.overrides[customerID+":"+ruleID]
	if !ok {
		return domain.CustomerPriceOverride{}, fmt.Errorf("not found")
	}
	return o, nil
}

type mockInvoices struct {
	invoices  []domain.Invoice
	lineItems []domain.InvoiceLineItem
}

func (m *mockInvoices) CreateInvoice(_ context.Context, tenantID string, inv domain.Invoice) (domain.Invoice, error) {
	inv.ID = fmt.Sprintf("vlx_inv_%d", len(m.invoices)+1)
	inv.TenantID = tenantID
	m.invoices = append(m.invoices, inv)
	return inv, nil
}

func (m *mockInvoices) CreateLineItem(_ context.Context, tenantID string, item domain.InvoiceLineItem) (domain.InvoiceLineItem, error) {
	item.ID = fmt.Sprintf("vlx_ili_%d", len(m.lineItems)+1)
	item.TenantID = tenantID
	m.lineItems = append(m.lineItems, item)
	return item, nil
}

func (m *mockInvoices) ApplyCreditAmount(_ context.Context, _, id string, amountCents int64) (domain.Invoice, error) {
	for i, inv := range m.invoices {
		if inv.ID == id {
			m.invoices[i].AmountDueCents -= amountCents
			return m.invoices[i], nil
		}
	}
	return domain.Invoice{}, fmt.Errorf("not found")
}

func (m *mockInvoices) GetInvoice(_ context.Context, _, id string) (domain.Invoice, error) {
	for _, inv := range m.invoices {
		if inv.ID == id {
			return inv, nil
		}
	}
	return domain.Invoice{}, fmt.Errorf("not found")
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func setupEngine() (*Engine, *mockSubs, *mockUsage, *mockPricing, *mockInvoices) {
	periodStart := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	nextBilling := periodEnd

	subs := &mockSubs{
		subs: map[string]domain.Subscription{
			"sub_1": {
				ID: "sub_1", TenantID: "t1", CustomerID: "cus_1", PlanID: "pln_1",
				Status: domain.SubscriptionActive, BillingTime: domain.BillingTimeCalendar,
				CurrentBillingPeriodStart: &periodStart, CurrentBillingPeriodEnd: &periodEnd,
				NextBillingAt: &nextBilling,
			},
		},
		cycleUpdated: make(map[string]bool),
	}

	usage := &mockUsage{
		totals: map[string]int64{
			"mtr_api":     1500,
			"mtr_storage": 250,
		},
	}

	pricing := &mockPricing{
		plans: map[string]domain.Plan{
			"pln_1": {
				ID: "pln_1", Name: "Pro Plan", Currency: "USD",
				BillingInterval: domain.BillingMonthly,
				BaseAmountCents: 4900,
				MeterIDs:        []string{"mtr_api", "mtr_storage"},
			},
		},
		meters: map[string]domain.Meter{
			"mtr_api":     {ID: "mtr_api", Name: "API Calls", Unit: "calls", RatingRuleVersionID: "rrv_api"},
			"mtr_storage": {ID: "mtr_storage", Name: "Storage", Unit: "GB", RatingRuleVersionID: "rrv_storage"},
		},
		rules: map[string]domain.RatingRuleVersion{
			"rrv_api": {
				ID: "rrv_api", Mode: domain.PricingGraduated,
				GraduatedTiers: []domain.RatingTier{
					{UpTo: 1000, UnitAmountCents: 10},
					{UpTo: 0, UnitAmountCents: 5},
				},
			},
			"rrv_storage": {
				ID: "rrv_storage", Mode: domain.PricingFlat,
				FlatAmountCents: 2500,
			},
		},
	}

	invoices := &mockInvoices{}

	engine := NewEngine(subs, usage, pricing, invoices, nil, nil, nil, nil)
	return engine, subs, usage, pricing, invoices
}

func TestRunCycle_GeneratesInvoice(t *testing.T) {
	engine, subs, _, _, invoices := setupEngine()
	ctx := context.Background()

	count, errs := engine.RunCycle(ctx, 50)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if count != 1 {
		t.Fatalf("got %d invoices, want 1", count)
	}

	// Verify invoice
	if len(invoices.invoices) != 1 {
		t.Fatalf("got %d invoices stored, want 1", len(invoices.invoices))
	}

	inv := invoices.invoices[0]
	if inv.CustomerID != "cus_1" {
		t.Errorf("got customer_id %q, want cus_1", inv.CustomerID)
	}
	if inv.SubscriptionID != "sub_1" {
		t.Errorf("got subscription_id %q, want sub_1", inv.SubscriptionID)
	}
	if inv.Status != domain.InvoiceFinalized {
		t.Errorf("got status %q, want finalized", inv.Status)
	}
	if inv.Currency != "USD" {
		t.Errorf("got currency %q, want USD", inv.Currency)
	}

	// Expected: base fee $49 + API graduated (1000*10 + 500*5 = 12500) + storage flat (250*2500 = 625000)
	// Total: 4900 + 12500 + 625000 = 642400 cents
	expectedTotal := int64(4900 + 12500 + 625000)
	if inv.TotalAmountCents != expectedTotal {
		t.Errorf("got total %d cents, want %d", inv.TotalAmountCents, expectedTotal)
	}

	// Verify line items (base + 2 usage)
	if len(invoices.lineItems) != 3 {
		t.Fatalf("got %d line items, want 3", len(invoices.lineItems))
	}

	// Verify billing cycle was advanced
	if !subs.cycleUpdated["sub_1"] {
		t.Error("billing cycle should have been advanced")
	}
	updatedSub := subs.subs["sub_1"]
	if updatedSub.CurrentBillingPeriodStart.Month() != time.April {
		t.Errorf("next period should start in April, got %v", updatedSub.CurrentBillingPeriodStart)
	}
}

func TestRunCycle_NoDueSubscriptions(t *testing.T) {
	engine, subs, _, _, _ := setupEngine()

	// Move next_billing_at to the future
	s := subs.subs["sub_1"]
	future := time.Now().UTC().AddDate(0, 1, 0)
	s.NextBillingAt = &future
	subs.subs["sub_1"] = s

	count, errs := engine.RunCycle(context.Background(), 50)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if count != 0 {
		t.Errorf("got %d, want 0 invoices", count)
	}
}

func TestRunCycle_SkipsTrialSubscription(t *testing.T) {
	engine, subs, _, _, invoices := setupEngine()

	// Set trial that hasn't ended yet
	s := subs.subs["sub_1"]
	trialEnd := time.Now().UTC().AddDate(0, 0, 7) // 7 days from now
	s.TrialEndAt = &trialEnd
	subs.subs["sub_1"] = s

	count, errs := engine.RunCycle(context.Background(), 50)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if count != 0 {
		t.Errorf("got %d, want 0 (trial should be skipped)", count)
	}
	if len(invoices.invoices) != 0 {
		t.Error("no invoice should be generated during trial")
	}
	// But billing cycle should still advance
	if !subs.cycleUpdated["sub_1"] {
		t.Error("billing cycle should advance even during trial")
	}
}

func TestRunCycle_ZeroUsage(t *testing.T) {
	engine, _, usage, _, invoices := setupEngine()

	// No usage at all
	usage.totals = map[string]int64{}

	count, errs := engine.RunCycle(context.Background(), 50)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if count != 1 {
		t.Fatalf("got %d, want 1 (base fee invoice)", count)
	}

	// Should still generate invoice with just the base fee
	inv := invoices.invoices[0]
	if inv.TotalAmountCents != 4900 {
		t.Errorf("got total %d, want 4900 (base fee only)", inv.TotalAmountCents)
	}
	if len(invoices.lineItems) != 1 {
		t.Errorf("got %d line items, want 1 (base fee only)", len(invoices.lineItems))
	}
}

func TestRunCycle_LineItemDetails(t *testing.T) {
	engine, _, _, _, invoices := setupEngine()

	engine.RunCycle(context.Background(), 50)

	// Find usage line items
	for _, item := range invoices.lineItems {
		switch item.LineType {
		case domain.LineTypeBaseFee:
			if item.AmountCents != 4900 {
				t.Errorf("base fee: got %d, want 4900", item.AmountCents)
			}
			if item.Quantity != 1 {
				t.Errorf("base fee quantity: got %d, want 1", item.Quantity)
			}

		case domain.LineTypeUsage:
			if item.MeterID == "mtr_api" {
				if item.Quantity != 1500 {
					t.Errorf("API quantity: got %d, want 1500", item.Quantity)
				}
				if item.AmountCents != 12500 {
					t.Errorf("API amount: got %d, want 12500", item.AmountCents)
				}
				if item.PricingMode != "graduated" {
					t.Errorf("API pricing_mode: got %q, want graduated", item.PricingMode)
				}
				if item.RatingRuleVersionID != "rrv_api" {
					t.Errorf("API rule_id: got %q, want rrv_api", item.RatingRuleVersionID)
				}
			}
			if item.MeterID == "mtr_storage" {
				if item.AmountCents != 625000 { // 250 qty * 2500 flat rate
					t.Errorf("storage amount: got %d, want 625000", item.AmountCents)
				}
				if item.PricingMode != "flat" {
					t.Errorf("storage pricing_mode: got %q, want flat", item.PricingMode)
				}
			}
		}
	}
}

func TestRunCycle_WithPriceOverride(t *testing.T) {
	engine, _, _, pricing, invoices := setupEngine()

	// Set a per-customer override: API calls flat $50 instead of graduated
	pricing.overrides = map[string]domain.CustomerPriceOverride{
		"cus_1:rrv_api": {
			ID: "cpo_1", CustomerID: "cus_1", RatingRuleVersionID: "rrv_api",
			Mode: domain.PricingFlat, FlatAmountCents: 5000,
			Active: true,
		},
	}

	count, errs := engine.RunCycle(context.Background(), 50)
	if len(errs) > 0 {
		t.Fatalf("errors: %v", errs)
	}
	if count != 1 {
		t.Fatalf("expected 1 invoice, got %d", count)
	}

	inv := invoices.invoices[0]
	// Expected: base $49 + API flat (1500*5000=7500000) + storage flat (250*2500=625000)
	expectedTotal := int64(4900 + 7500000 + 625000)
	if inv.TotalAmountCents != expectedTotal {
		t.Errorf("with override: got %d cents, want %d",
			inv.TotalAmountCents, expectedTotal)
	}
}

func TestAdvanceBillingPeriod(t *testing.T) {
	from := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	monthly := advanceBillingPeriod(from, domain.BillingMonthly)
	if monthly.Month() != time.April || monthly.Day() != 1 {
		t.Errorf("monthly: got %v, want 2026-04-01", monthly)
	}

	yearly := advanceBillingPeriod(from, domain.BillingYearly)
	if yearly.Year() != 2027 || yearly.Month() != time.March {
		t.Errorf("yearly: got %v, want 2027-03-01", yearly)
	}
}
