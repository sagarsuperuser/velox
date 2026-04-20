package billing

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/clock"
)

// stubClockReader is a TestClockReader spy used by TestEffectiveNow. It
// returns a canned (TestClock, error) pair and records the last ID queried so
// the test can assert the engine looked up the right clock.
type stubClockReader struct {
	clk      domain.TestClock
	err      error
	lastID   string
	lastTenant string
}

func (s *stubClockReader) Get(_ context.Context, tenantID, id string) (domain.TestClock, error) {
	s.lastID = id
	s.lastTenant = tenantID
	if s.err != nil {
		return domain.TestClock{}, s.err
	}
	return s.clk, nil
}

// mockSettings is a minimal SettingsReader for engine tests. Hands out
// VLX-000001, VLX-000002, ... deterministically; Get returns a zero-value
// TenantSettings so net_payment_terms defaults to the engine's fallback.
type mockSettings struct{ next int }

func (m *mockSettings) NextInvoiceNumber(_ context.Context, _ string) (string, error) {
	m.next++
	return fmt.Sprintf("VLX-%06d", m.next), nil
}

func (m *mockSettings) Get(_ context.Context, _ string) (domain.TenantSettings, error) {
	return domain.TenantSettings{}, nil
}

// ---------------------------------------------------------------------------
// In-memory implementations of the billing engine's interfaces
// ---------------------------------------------------------------------------

type mockSubs struct {
	subs         map[string]domain.Subscription
	cycleUpdated map[string]bool
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

func (m *mockSubs) ApplyPendingPlanAtomic(_ context.Context, _, id string, now time.Time) (domain.Subscription, error) {
	s, ok := m.subs[id]
	if !ok {
		return domain.Subscription{}, errs.ErrNotFound
	}
	if s.PendingPlanID == "" || s.PendingPlanEffectiveAt == nil || s.PendingPlanEffectiveAt.After(now) {
		return domain.Subscription{}, errs.ErrNotFound
	}
	s.PreviousPlanID = s.PlanID
	s.PlanID = s.PendingPlanID
	s.PlanChangedAt = &now
	s.PendingPlanID = ""
	s.PendingPlanEffectiveAt = nil
	m.subs[id] = s
	return s, nil
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

func (m *mockUsage) AggregateForBillingPeriodByAgg(_ context.Context, _, _ string, meters map[string]string, _, _ time.Time) (map[string]int64, error) {
	result := make(map[string]int64)
	for id := range meters {
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

func (m *mockPricing) GetLatestRuleByKey(_ context.Context, _, ruleKey string) (domain.RatingRuleVersion, error) {
	// Return the latest version with matching key
	var latest domain.RatingRuleVersion
	found := false
	for _, r := range m.rules {
		if r.RuleKey == ruleKey {
			if !found || r.Version > latest.Version {
				latest = r
				found = true
			}
		}
	}
	if !found {
		return domain.RatingRuleVersion{}, fmt.Errorf("rule not found for key %s", ruleKey)
	}
	return latest, nil
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
			m.invoices[i].CreditsAppliedCents += amountCents
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

func (m *mockInvoices) MarkPaid(_ context.Context, _, id string, stripePI string, paidAt time.Time) (domain.Invoice, error) {
	for i, inv := range m.invoices {
		if inv.ID == id {
			m.invoices[i].Status = domain.InvoicePaid
			m.invoices[i].PaymentStatus = domain.PaymentSucceeded
			m.invoices[i].AmountPaidCents = m.invoices[i].AmountDueCents
			m.invoices[i].AmountDueCents = 0
			m.invoices[i].PaidAt = &paidAt
			return m.invoices[i], nil
		}
	}
	return domain.Invoice{}, fmt.Errorf("not found")
}

func (m *mockInvoices) CreateInvoiceWithLineItems(_ context.Context, tenantID string, inv domain.Invoice, items []domain.InvoiceLineItem) (domain.Invoice, error) {
	inv.ID = fmt.Sprintf("vlx_inv_%d", len(m.invoices)+1)
	inv.TenantID = tenantID
	m.invoices = append(m.invoices, inv)
	for _, item := range items {
		item.InvoiceID = inv.ID
		item.ID = fmt.Sprintf("vlx_ili_%d", len(m.lineItems)+1)
		item.TenantID = tenantID
		m.lineItems = append(m.lineItems, item)
	}
	return inv, nil
}

func (m *mockInvoices) SetAutoChargePending(_ context.Context, _, id string, pending bool) error {
	for i, inv := range m.invoices {
		if inv.ID == id {
			m.invoices[i].AutoChargePending = pending
			return nil
		}
	}
	return fmt.Errorf("not found")
}

func (m *mockInvoices) ListAutoChargePending(_ context.Context, limit int) ([]domain.Invoice, error) {
	var result []domain.Invoice
	for _, inv := range m.invoices {
		if inv.AutoChargePending {
			result = append(result, inv)
			if len(result) >= limit {
				break
			}
		}
	}
	return result, nil
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
				ID: "rrv_api", RuleKey: "api_calls", Version: 1, Mode: domain.PricingGraduated,
				GraduatedTiers: []domain.RatingTier{
					{UpTo: 1000, UnitAmountCents: 10},
					{UpTo: 0, UnitAmountCents: 5},
				},
			},
			"rrv_storage": {
				ID: "rrv_storage", RuleKey: "storage_gb", Version: 1, Mode: domain.PricingFlat,
				FlatAmountCents: 2500,
			},
		},
	}

	invoices := &mockInvoices{}

	engine := NewEngine(subs, usage, pricing, invoices, nil, &mockSettings{}, nil, nil, nil)
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

// TestRunCycle_UnitAmountRoundsBankers is the COR-5 regression: the rating
// path previously used `amount / quantity` (truncating int division) to
// derive the per-unit display amount. For graduated/tiered rules the total
// rarely divides cleanly by the quantity, and truncation biased every
// displayed unit price downward — systematic over large batches, which
// accountants catch. Switching to money.RoundHalfToEven produces the
// nearest-cent unit while preserving the true AmountCents total.
//
// Setup: graduated rule with tier 1 (up to 1 unit) at 100 cents and tier 2
// at 50 cents per unit. Usage = 3 → amount = 1*100 + 2*50 = 200 cents.
// Truncation: 200/3 = 66. Banker's: 66.67 → 67.
func TestRunCycle_UnitAmountRoundsBankers(t *testing.T) {
	periodStart := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	subs := &mockSubs{
		subs: map[string]domain.Subscription{
			"sub_1": {
				ID: "sub_1", TenantID: "t1", CustomerID: "cus_1", PlanID: "pln_1",
				Status: domain.SubscriptionActive, BillingTime: domain.BillingTimeCalendar,
				CurrentBillingPeriodStart: &periodStart, CurrentBillingPeriodEnd: &periodEnd,
				NextBillingAt: &periodEnd,
			},
		},
		cycleUpdated: make(map[string]bool),
	}
	usage := &mockUsage{totals: map[string]int64{"mtr_api": 3}}
	pricing := &mockPricing{
		plans: map[string]domain.Plan{
			"pln_1": {
				ID: "pln_1", Name: "Round Plan", Currency: "USD",
				BillingInterval: domain.BillingMonthly,
				BaseAmountCents: 0, MeterIDs: []string{"mtr_api"},
			},
		},
		meters: map[string]domain.Meter{
			"mtr_api": {ID: "mtr_api", Name: "API Calls", Unit: "calls", RatingRuleVersionID: "rrv_api"},
		},
		rules: map[string]domain.RatingRuleVersion{
			"rrv_api": {
				ID: "rrv_api", RuleKey: "api_calls", Version: 1, Mode: domain.PricingGraduated,
				GraduatedTiers: []domain.RatingTier{
					{UpTo: 1, UnitAmountCents: 100},
					{UpTo: 0, UnitAmountCents: 50},
				},
			},
		},
	}
	invoices := &mockInvoices{}
	engine := NewEngine(subs, usage, pricing, invoices, nil, &mockSettings{}, nil, nil, nil)

	if _, errs := engine.RunCycle(context.Background(), 50); len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(invoices.invoices) != 1 {
		t.Fatalf("got %d invoices, want 1", len(invoices.invoices))
	}

	var usageLine *domain.InvoiceLineItem
	for i := range invoices.lineItems {
		if invoices.lineItems[i].MeterID == "mtr_api" {
			usageLine = &invoices.lineItems[i]
			break
		}
	}
	if usageLine == nil {
		t.Fatal("api usage line not found in invoice")
	}

	if usageLine.AmountCents != 200 {
		t.Errorf("amount_cents: got %d, want 200 (1*100 + 2*50)", usageLine.AmountCents)
	}
	if usageLine.Quantity != 3 {
		t.Errorf("quantity: got %d, want 3", usageLine.Quantity)
	}
	// Truncation would give 66; banker's rounding of 200/3 = 66.67 gives 67.
	if usageLine.UnitAmountCents != 67 {
		t.Errorf("unit_amount_cents: got %d, want 67 (banker's round of 200/3)",
			usageLine.UnitAmountCents)
	}
}

// A subscription with a due pending plan change must bill on the new plan
// after ApplyPendingPlanAtomic runs at the top of billSubscription. Locks in
// the COR-3 contract: the cycle boundary is the swap point.
func TestRunCycle_AppliesScheduledPlanChangeAtBoundary(t *testing.T) {
	periodStart := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	// Effective exactly at periodEnd — the partial-index WHERE clause requires
	// effective_at <= now, and the cycle runs at periodEnd.
	effectiveAt := periodEnd

	subs := &mockSubs{
		subs: map[string]domain.Subscription{
			"sub_1": {
				ID: "sub_1", TenantID: "t1", CustomerID: "cus_1", PlanID: "pln_old",
				Status: domain.SubscriptionActive, BillingTime: domain.BillingTimeCalendar,
				CurrentBillingPeriodStart: &periodStart, CurrentBillingPeriodEnd: &periodEnd,
				NextBillingAt:             &periodEnd,
				PendingPlanID:             "pln_new",
				PendingPlanEffectiveAt:    &effectiveAt,
			},
		},
		cycleUpdated: make(map[string]bool),
	}

	pricing := &mockPricing{
		plans: map[string]domain.Plan{
			"pln_old": {ID: "pln_old", Name: "Old", Currency: "USD", BillingInterval: domain.BillingMonthly, BaseAmountCents: 1000},
			"pln_new": {ID: "pln_new", Name: "New", Currency: "USD", BillingInterval: domain.BillingMonthly, BaseAmountCents: 3000},
		},
	}

	invoices := &mockInvoices{}
	usage := &mockUsage{totals: map[string]int64{}}
	engine := NewEngine(subs, usage, pricing, invoices, nil, &mockSettings{}, nil, nil, nil)

	count, errs := engine.RunCycle(context.Background(), 50)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if count != 1 {
		t.Fatalf("got %d invoices, want 1", count)
	}

	inv := invoices.invoices[0]
	if inv.SubtotalCents != 3000 {
		t.Errorf("billed on wrong plan: subtotal %d cents, want 3000 (new plan)", inv.SubtotalCents)
	}

	// Sub row must reflect the swap.
	got := subs.subs["sub_1"]
	if got.PlanID != "pln_new" {
		t.Errorf("plan_id: got %q, want pln_new", got.PlanID)
	}
	if got.PreviousPlanID != "pln_old" {
		t.Errorf("previous_plan_id: got %q, want pln_old", got.PreviousPlanID)
	}
	if got.PendingPlanID != "" || got.PendingPlanEffectiveAt != nil {
		t.Errorf("pending fields should be cleared: got pending_id=%q effective_at=%v",
			got.PendingPlanID, got.PendingPlanEffectiveAt)
	}
}

// A pending change dated in the future must NOT apply at the current cycle —
// the billing engine bills on the existing plan and leaves pending fields intact.
func TestRunCycle_SkipsPendingChangeNotYetDue(t *testing.T) {
	periodStart := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	futureEffective := periodEnd.AddDate(0, 1, 0) // 2026-05-01, next cycle

	subs := &mockSubs{
		subs: map[string]domain.Subscription{
			"sub_1": {
				ID: "sub_1", TenantID: "t1", CustomerID: "cus_1", PlanID: "pln_old",
				Status: domain.SubscriptionActive, BillingTime: domain.BillingTimeCalendar,
				CurrentBillingPeriodStart: &periodStart, CurrentBillingPeriodEnd: &periodEnd,
				NextBillingAt:             &periodEnd,
				PendingPlanID:             "pln_new",
				PendingPlanEffectiveAt:    &futureEffective,
			},
		},
		cycleUpdated: make(map[string]bool),
	}

	pricing := &mockPricing{
		plans: map[string]domain.Plan{
			"pln_old": {ID: "pln_old", Name: "Old", Currency: "USD", BillingInterval: domain.BillingMonthly, BaseAmountCents: 1000},
			"pln_new": {ID: "pln_new", Name: "New", Currency: "USD", BillingInterval: domain.BillingMonthly, BaseAmountCents: 3000},
		},
	}

	invoices := &mockInvoices{}
	usage := &mockUsage{totals: map[string]int64{}}
	engine := NewEngine(subs, usage, pricing, invoices, nil, &mockSettings{}, nil, nil, nil)

	_, errs := engine.RunCycle(context.Background(), 50)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	inv := invoices.invoices[0]
	if inv.SubtotalCents != 1000 {
		t.Errorf("should have billed on old plan: subtotal %d, want 1000", inv.SubtotalCents)
	}

	got := subs.subs["sub_1"]
	if got.PendingPlanID != "pln_new" {
		t.Errorf("pending change should be preserved: got %q", got.PendingPlanID)
	}
	if got.PlanID != "pln_old" {
		t.Errorf("plan_id should not have swapped: got %q", got.PlanID)
	}
}

// TestEffectiveNow covers the four branches of Engine.effectiveNow:
// no test clock → wall-clock; test clock wired → frozen_time; clock id set
// but no reader → wall-clock; reader errors → wall-clock. These branches
// are what makes a test-mode sub bill at simulated time without affecting
// live subs on the same engine instance.
func TestEffectiveNow(t *testing.T) {
	wall := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)
	frozen := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fakeClk := clock.NewFake(wall)

	liveSub := domain.Subscription{ID: "s_live", TenantID: "t1"}
	testSub := domain.Subscription{ID: "s_test", TenantID: "t1", TestClockID: "tc_1"}

	t.Run("no test clock id returns wall clock", func(t *testing.T) {
		e := NewEngine(nil, nil, nil, nil, nil, nil, nil, nil, fakeClk)
		e.SetTestClockReader(&stubClockReader{clk: domain.TestClock{FrozenTime: frozen}})
		got := e.effectiveNow(context.Background(), liveSub)
		if !got.Equal(wall) {
			t.Errorf("wall-clock sub: got %v, want %v", got, wall)
		}
	})

	t.Run("test clock id with reader returns frozen time", func(t *testing.T) {
		reader := &stubClockReader{clk: domain.TestClock{FrozenTime: frozen}}
		e := NewEngine(nil, nil, nil, nil, nil, nil, nil, nil, fakeClk)
		e.SetTestClockReader(reader)
		got := e.effectiveNow(context.Background(), testSub)
		if !got.Equal(frozen) {
			t.Errorf("test-mode sub: got %v, want %v", got, frozen)
		}
		if reader.lastID != "tc_1" || reader.lastTenant != "t1" {
			t.Errorf("reader lookup: got (%q,%q), want (t1,tc_1)",
				reader.lastTenant, reader.lastID)
		}
	})

	t.Run("test clock id without reader falls back to wall clock", func(t *testing.T) {
		e := NewEngine(nil, nil, nil, nil, nil, nil, nil, nil, fakeClk)
		got := e.effectiveNow(context.Background(), testSub)
		if !got.Equal(wall) {
			t.Errorf("nil-reader fallback: got %v, want %v", got, wall)
		}
	})

	t.Run("reader error falls back to wall clock", func(t *testing.T) {
		e := NewEngine(nil, nil, nil, nil, nil, nil, nil, nil, fakeClk)
		e.SetTestClockReader(&stubClockReader{err: errs.ErrNotFound})
		got := e.effectiveNow(context.Background(), testSub)
		if !got.Equal(wall) {
			t.Errorf("error fallback: got %v, want %v", got, wall)
		}
	})
}
