package billing

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/clock"
)

// stubClockReader is a TestClockReader spy used by TestEffectiveNow. It
// returns a canned (TestClock, error) pair and records the last ID queried so
// the test can assert the engine looked up the right clock.
type stubClockReader struct {
	clk        domain.TestClock
	err        error
	lastID     string
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
		eligible := s.Status == domain.SubscriptionActive || s.Status == domain.SubscriptionTrialing
		if eligible && s.NextBillingAt != nil && !s.NextBillingAt.After(before) {
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

func (m *mockSubs) FireScheduledCancellation(_ context.Context, _, id string, at time.Time) (domain.Subscription, error) {
	s, ok := m.subs[id]
	if !ok {
		return domain.Subscription{}, errs.ErrNotFound
	}
	if s.Status != domain.SubscriptionActive {
		return domain.Subscription{}, errs.ErrInvalidState
	}
	s.Status = domain.SubscriptionCanceled
	atCopy := at
	s.CanceledAt = &atCopy
	s.NextBillingAt = nil
	s.CancelAt = nil
	s.CancelAtPeriodEnd = false
	m.subs[id] = s
	return s, nil
}

func (m *mockSubs) ClearPauseCollection(_ context.Context, _, id string) (domain.Subscription, error) {
	s, ok := m.subs[id]
	if !ok {
		return domain.Subscription{}, errs.ErrNotFound
	}
	s.PauseCollection = nil
	m.subs[id] = s
	return s, nil
}

func (m *mockSubs) ActivateAfterTrial(_ context.Context, _, id string, at time.Time) (domain.Subscription, error) {
	s, ok := m.subs[id]
	if !ok {
		return domain.Subscription{}, errs.ErrNotFound
	}
	if s.Status != domain.SubscriptionTrialing {
		return domain.Subscription{}, errs.InvalidState("not trialing")
	}
	s.Status = domain.SubscriptionActive
	if s.ActivatedAt == nil {
		t := at
		s.ActivatedAt = &t
	}
	m.subs[id] = s
	return s, nil
}

// ApplyDuePendingItemPlansAtomic mirrors the postgres store: for every item on
// the subscription whose pending change is due (effective_at <= now), swap
// plan_id ← pending_plan_id and clear the pending fields in one pass. Returns
// the applied items so the engine can audit which swaps landed at this cycle.
func (m *mockSubs) ApplyDuePendingItemPlansAtomic(_ context.Context, _, id string, now time.Time) ([]domain.SubscriptionItem, error) {
	s, ok := m.subs[id]
	if !ok {
		return nil, errs.ErrNotFound
	}
	var applied []domain.SubscriptionItem
	for i := range s.Items {
		it := s.Items[i]
		if it.PendingPlanID == "" || it.PendingPlanEffectiveAt == nil || it.PendingPlanEffectiveAt.After(now) {
			continue
		}
		it.PlanID = it.PendingPlanID
		it.PlanChangedAt = &now
		it.PendingPlanID = ""
		it.PendingPlanEffectiveAt = nil
		s.Items[i] = it
		applied = append(applied, it)
	}
	m.subs[id] = s
	return applied, nil
}

func (m *mockSubs) ListWithThresholds(_ context.Context, _ bool, _ int) ([]domain.Subscription, error) {
	// Engine unit tests focus on the natural cycle; the threshold scan path
	// is exercised via threshold_scan_test.go (which uses its own mock that
	// returns the configured candidate set). Returning empty here keeps
	// existing tests compatible without exercising the threshold path.
	return nil, nil
}

type mockUsage struct {
	totals map[string]int64 // meterID -> quantity (test inputs stay int for readability)
}

func (m *mockUsage) AggregateForBillingPeriod(_ context.Context, _, _ string, meterIDs []string, _, _ time.Time) (map[string]decimal.Decimal, error) {
	result := make(map[string]decimal.Decimal)
	for _, id := range meterIDs {
		if qty, ok := m.totals[id]; ok {
			result[id] = decimal.NewFromInt(qty)
		}
	}
	return result, nil
}

func (m *mockUsage) AggregateForBillingPeriodByAgg(_ context.Context, _, _ string, meters map[string]string, _, _ time.Time) (map[string]decimal.Decimal, error) {
	result := make(map[string]decimal.Decimal)
	for id := range meters {
		if qty, ok := m.totals[id]; ok {
			result[id] = decimal.NewFromInt(qty)
		}
	}
	return result, nil
}

// AggregateByPricingRules is a minimal stub — engine_test.go's existing
// preview tests don't exercise the multi-dim path; the create_preview
// integration tests cover that against real Postgres. We return one
// aggregation per known meter so the new preview path produces the same
// totals the legacy tests expect.
func (m *mockUsage) AggregateByPricingRules(_ context.Context, _, _, meterID string, _ domain.AggregationMode, _, _ time.Time) ([]domain.RuleAggregation, error) {
	qty, ok := m.totals[meterID]
	if !ok {
		return nil, nil
	}
	return []domain.RuleAggregation{{
		RuleID:              "",
		RatingRuleVersionID: "",
		Quantity:            decimal.NewFromInt(qty),
	}}, nil
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

// ListMeterPricingRulesByMeter is a no-op stub. The engine unit tests use
// single-rule meters; per-rule DimensionMatch echo is covered by the
// create_preview integration tests against real Postgres.
func (m *mockPricing) ListMeterPricingRulesByMeter(_ context.Context, _, _ string) ([]domain.MeterPricingRule, error) {
	return nil, nil
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

func (m *mockInvoices) SetTaxTransaction(_ context.Context, _, id string, taxTransactionID string) error {
	for i, inv := range m.invoices {
		if inv.ID == id {
			m.invoices[i].TaxTransactionID = taxTransactionID
			return nil
		}
	}
	return fmt.Errorf("not found")
}

func (m *mockInvoices) ListLineItems(_ context.Context, _, invoiceID string) ([]domain.InvoiceLineItem, error) {
	var out []domain.InvoiceLineItem
	for _, li := range m.lineItems {
		if li.InvoiceID == invoiceID {
			out = append(out, li)
		}
	}
	return out, nil
}

func (m *mockInvoices) ApplyDiscountAtomic(_ context.Context, tenantID, invoiceID string, update domain.InvoiceDiscountUpdate, lineItems []domain.InvoiceLineItem) (domain.Invoice, error) {
	idx := -1
	for i, inv := range m.invoices {
		if inv.ID == invoiceID && inv.TenantID == tenantID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return domain.Invoice{}, errs.ErrNotFound
	}
	inv := m.invoices[idx]
	if inv.Status != domain.InvoiceDraft {
		return domain.Invoice{}, errs.InvalidState(fmt.Sprintf("invoice must be draft (current: %s)", inv.Status))
	}
	if inv.DiscountCents > 0 {
		return domain.Invoice{}, errs.InvalidState("invoice already has a discount applied")
	}
	byID := make(map[string]domain.InvoiceLineItem, len(lineItems))
	for _, li := range lineItems {
		byID[li.ID] = li
	}
	for i, existing := range m.lineItems {
		if existing.InvoiceID != invoiceID {
			continue
		}
		if updated, ok := byID[existing.ID]; ok {
			m.lineItems[i].AmountCents = updated.AmountCents
			m.lineItems[i].TaxRateBP = updated.TaxRateBP
			m.lineItems[i].TaxAmountCents = updated.TaxAmountCents
			m.lineItems[i].TotalAmountCents = updated.TotalAmountCents
		}
	}
	inv.SubtotalCents = update.SubtotalCents
	inv.DiscountCents = update.DiscountCents
	inv.TaxAmountCents = update.TaxAmountCents
	inv.TaxRateBP = update.TaxRateBP
	inv.TaxName = update.TaxName
	inv.TaxCountry = update.TaxCountry
	inv.TaxID = update.TaxID
	inv.TaxProvider = update.TaxProvider
	inv.TaxCalculationID = update.TaxCalculationID
	inv.TaxReverseCharge = update.TaxReverseCharge
	inv.TaxExemptReason = update.TaxExemptReason
	inv.TaxStatus = update.TaxStatus
	inv.TaxDeferredAt = update.TaxDeferredAt
	inv.TaxPendingReason = update.TaxPendingReason
	inv.TotalAmountCents = update.SubtotalCents - update.DiscountCents + update.TaxAmountCents
	due := inv.TotalAmountCents - inv.AmountPaidCents - inv.CreditsAppliedCents
	if due < 0 {
		due = 0
	}
	inv.AmountDueCents = due
	m.invoices[idx] = inv
	return inv, nil
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
				ID: "sub_1", TenantID: "t1", CustomerID: "cus_1",
				Items:  []domain.SubscriptionItem{{PlanID: "pln_1", Quantity: 1}},
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

	// New state-machine semantics: a trial is conveyed by status='trialing',
	// not just by trial_end_at being set. Service.Create routes new subs with
	// trial_days > 0 to trialing; the engine skips billing while they're in
	// that state.
	s := subs.subs["sub_1"]
	trialEnd := time.Now().UTC().AddDate(0, 0, 7) // 7 days from now
	s.Status = domain.SubscriptionTrialing
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
				ID: "sub_1", TenantID: "t1", CustomerID: "cus_1",
				Items:  []domain.SubscriptionItem{{PlanID: "pln_1", Quantity: 1}},
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
				ID: "sub_1", TenantID: "t1", CustomerID: "cus_1",
				Items: []domain.SubscriptionItem{{
					PlanID:                 "pln_old",
					Quantity:               1,
					PendingPlanID:          "pln_new",
					PendingPlanEffectiveAt: &effectiveAt,
				}},
				Status: domain.SubscriptionActive, BillingTime: domain.BillingTimeCalendar,
				CurrentBillingPeriodStart: &periodStart, CurrentBillingPeriodEnd: &periodEnd,
				NextBillingAt: &periodEnd,
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

	// Item row must reflect the swap.
	got := subs.subs["sub_1"]
	if len(got.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(got.Items))
	}
	it := got.Items[0]
	if it.PlanID != "pln_new" {
		t.Errorf("item plan_id: got %q, want pln_new", it.PlanID)
	}
	if it.PendingPlanID != "" || it.PendingPlanEffectiveAt != nil {
		t.Errorf("pending fields should be cleared: got pending_id=%q effective_at=%v",
			it.PendingPlanID, it.PendingPlanEffectiveAt)
	}
	if it.PlanChangedAt == nil {
		t.Error("plan_changed_at should be set after swap")
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
				ID: "sub_1", TenantID: "t1", CustomerID: "cus_1",
				Items: []domain.SubscriptionItem{{
					PlanID:                 "pln_old",
					Quantity:               1,
					PendingPlanID:          "pln_new",
					PendingPlanEffectiveAt: &futureEffective,
				}},
				Status: domain.SubscriptionActive, BillingTime: domain.BillingTimeCalendar,
				CurrentBillingPeriodStart: &periodStart, CurrentBillingPeriodEnd: &periodEnd,
				NextBillingAt: &periodEnd,
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

	it := subs.subs["sub_1"].Items[0]
	if it.PendingPlanID != "pln_new" {
		t.Errorf("pending change should be preserved: got %q", it.PendingPlanID)
	}
	if it.PlanID != "pln_old" {
		t.Errorf("plan_id should not have swapped: got %q", it.PlanID)
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

// capturingEventDispatcher records every Dispatch call for event-firing tests.
type capturingEventDispatcher struct {
	events []struct {
		tenantID  string
		eventType string
		payload   map[string]any
	}
}

func (d *capturingEventDispatcher) Dispatch(_ context.Context, tenantID, eventType string, payload map[string]any) error {
	d.events = append(d.events, struct {
		tenantID  string
		eventType string
		payload   map[string]any
	}{tenantID, eventType, payload})
	return nil
}

// TestRunCycle_FiresPendingChangeAppliedEvent locks in P0 #2: when a due
// pending item plan change rolls in at the cycle boundary, the engine must
// emit subscription.pending_change.applied per swapped item so downstream
// systems (analytics, revrec, customer notifications) don't have to poll for
// state diffs to know the change landed.
func TestRunCycle_FiresPendingChangeAppliedEvent(t *testing.T) {
	periodStart := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	effectiveAt := periodEnd

	subs := &mockSubs{
		subs: map[string]domain.Subscription{
			"sub_1": {
				ID: "sub_1", TenantID: "t1", CustomerID: "cus_1",
				Items: []domain.SubscriptionItem{{
					ID:                     "si_1",
					PlanID:                 "pln_old",
					Quantity:               1,
					PendingPlanID:          "pln_new",
					PendingPlanEffectiveAt: &effectiveAt,
				}},
				Status: domain.SubscriptionActive, BillingTime: domain.BillingTimeCalendar,
				CurrentBillingPeriodStart: &periodStart, CurrentBillingPeriodEnd: &periodEnd,
				NextBillingAt: &periodEnd,
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
	dispatcher := &capturingEventDispatcher{}
	engine := NewEngine(subs, usage, pricing, invoices, nil, &mockSettings{}, nil, nil, nil)
	engine.SetEventDispatcher(dispatcher)

	if _, errs := engine.RunCycle(context.Background(), 50); len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	var applied map[string]any
	for _, ev := range dispatcher.events {
		if ev.eventType == domain.EventSubscriptionPendingChangeApplied {
			applied = ev.payload
			if ev.tenantID != "t1" {
				t.Errorf("tenant_id: got %q, want t1", ev.tenantID)
			}
			break
		}
	}
	if applied == nil {
		types := make([]string, 0, len(dispatcher.events))
		for _, ev := range dispatcher.events {
			types = append(types, ev.eventType)
		}
		t.Fatalf("expected %s, got types=%v", domain.EventSubscriptionPendingChangeApplied, types)
	}
	if applied["item_id"] != "si_1" {
		t.Errorf("item_id: got %v, want si_1", applied["item_id"])
	}
	if applied["old_plan_id"] != "pln_old" {
		t.Errorf("old_plan_id: got %v, want pln_old", applied["old_plan_id"])
	}
	if applied["new_plan_id"] != "pln_new" {
		t.Errorf("new_plan_id: got %v, want pln_new", applied["new_plan_id"])
	}
}

// TestRunCycle_OneSubFailsOthersContinue asserts the batch loop's isolation
// guarantee: a single subscription with bad data (here: a plan referencing a
// meter whose rating rule is missing) must not prevent healthy subscriptions
// in the same batch from being invoiced. Without this guarantee, one broken
// customer could stall the entire billing cycle.
func TestRunCycle_OneSubFailsOthersContinue(t *testing.T) {
	periodStart := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	subs := &mockSubs{
		subs: map[string]domain.Subscription{
			"sub_ok": {
				ID: "sub_ok", TenantID: "t1", CustomerID: "cus_ok",
				Items:  []domain.SubscriptionItem{{ID: "si_ok", PlanID: "pln_ok", Quantity: 1}},
				Status: domain.SubscriptionActive, BillingTime: domain.BillingTimeCalendar,
				CurrentBillingPeriodStart: &periodStart, CurrentBillingPeriodEnd: &periodEnd,
				NextBillingAt: &periodEnd,
			},
			"sub_bad": {
				ID: "sub_bad", TenantID: "t1", CustomerID: "cus_bad",
				Items:  []domain.SubscriptionItem{{ID: "si_bad", PlanID: "pln_bad", Quantity: 1}},
				Status: domain.SubscriptionActive, BillingTime: domain.BillingTimeCalendar,
				CurrentBillingPeriodStart: &periodStart, CurrentBillingPeriodEnd: &periodEnd,
				NextBillingAt: &periodEnd,
			},
		},
		cycleUpdated: make(map[string]bool),
	}

	pricing := &mockPricing{
		plans: map[string]domain.Plan{
			"pln_ok":  {ID: "pln_ok", Name: "Flat", Currency: "USD", BillingInterval: domain.BillingMonthly, BaseAmountCents: 1000},
			"pln_bad": {ID: "pln_bad", Name: "Broken", Currency: "USD", BillingInterval: domain.BillingMonthly, MeterIDs: []string{"mtr_missing"}},
		},
		// mtr_missing references rrv_missing which isn't in rules — lookup fails
		meters: map[string]domain.Meter{
			"mtr_missing": {ID: "mtr_missing", Name: "Missing", RatingRuleVersionID: "rrv_missing"},
		},
	}
	invoices := &mockInvoices{}
	usage := &mockUsage{totals: map[string]int64{"mtr_missing": 100}}
	engine := NewEngine(subs, usage, pricing, invoices, nil, &mockSettings{}, nil, nil, nil)

	count, runErrs := engine.RunCycle(context.Background(), 50)

	if count != 1 {
		t.Errorf("got %d invoices, want 1 (only sub_ok should succeed)", count)
	}
	if len(runErrs) != 1 {
		t.Fatalf("got %d errors, want 1 (sub_bad should fail)", len(runErrs))
	}
	// Healthy subscription's cycle should have advanced despite the neighbor failing.
	if !subs.cycleUpdated["sub_ok"] {
		t.Error("sub_ok billing cycle should have advanced")
	}
	if len(invoices.invoices) != 1 {
		t.Fatalf("got %d invoices stored, want 1", len(invoices.invoices))
	}
	if invoices.invoices[0].SubscriptionID != "sub_ok" {
		t.Errorf("stored invoice should be for sub_ok, got %q", invoices.invoices[0].SubscriptionID)
	}
}

// TestRunCycle_TaxProviderErrorDefersInvoice asserts end-to-end that a tax
// provider failure during RunCycle produces a draft invoice with
// tax_status=pending rather than a finalized invoice with wrong tax.
// Finalize is guarded downstream; the retry worker is responsible for
// completing the calculation and transitioning draft → finalized.
func TestRunCycle_TaxProviderErrorDefersInvoice(t *testing.T) {
	engine, _, _, _, invoices := setupEngine()
	engine.SetTaxProviderResolver(stubResolver(&stubProvider{err: fmt.Errorf("stripe down")}))

	count, runErrs := engine.RunCycle(context.Background(), 50)
	if len(runErrs) > 0 {
		t.Fatalf("unexpected errors: %v", runErrs)
	}
	if count != 1 {
		t.Fatalf("got %d invoices, want 1", count)
	}
	inv := invoices.invoices[0]
	if inv.TaxAmountCents != 0 {
		t.Errorf("got tax %d, want 0 when calculation deferred", inv.TaxAmountCents)
	}
	if inv.Status != domain.InvoiceDraft {
		t.Errorf("deferred invoice must stay in draft, got status %q", inv.Status)
	}
	if inv.TaxStatus != domain.InvoiceTaxPending {
		t.Errorf("tax_status = %q, want pending", inv.TaxStatus)
	}
	if inv.TaxPendingReason == "" {
		t.Error("tax_pending_reason should capture the provider error")
	}
}

// mockCouponApplier captures which of ApplyToInvoice / ApplyToInvoiceForCustomer
// the engine called so tests can assert Stripe's precedence rule:
// subscription-scope beats customer-scope on the same invoice. Each call
// returns the scripted CouponDiscountResult so tests can simulate "no
// subscription coupon" (return zero) and trigger the fallback branch.
// markedRedemptions and markedCustomerDiscounts are populated by the two
// Mark* methods separately — tests assert the engine routes each scope to
// its own writer, since customer_discounts is its own table.
type mockCouponApplier struct {
	subResult               domain.CouponDiscountResult
	subErr                  error
	customerResult          domain.CouponDiscountResult
	customerErr             error
	subCalled               bool
	customerCalled          bool
	markedRedemptions       []string
	markedCustomerDiscounts []string
	redeemReq               domain.CouponRedeemRequest
	redeemResult            domain.CouponRedeemResult
	redeemErr               error
	voidedInvoices          []string
	voidErr                 error
}

func (m *mockCouponApplier) ApplyToInvoice(_ context.Context, _, _, _, _ string, _ []string, _ int64) (domain.CouponDiscountResult, error) {
	m.subCalled = true
	return m.subResult, m.subErr
}

func (m *mockCouponApplier) ApplyToInvoiceForCustomer(_ context.Context, _, _, _ string, _ []string, _ int64) (domain.CouponDiscountResult, error) {
	m.customerCalled = true
	return m.customerResult, m.customerErr
}

func (m *mockCouponApplier) MarkPeriodsApplied(_ context.Context, _ string, ids []string) error {
	m.markedRedemptions = append(m.markedRedemptions, ids...)
	return nil
}

func (m *mockCouponApplier) MarkCustomerDiscountPeriodsApplied(_ context.Context, _ string, ids []string) error {
	m.markedCustomerDiscounts = append(m.markedCustomerDiscounts, ids...)
	return nil
}

func (m *mockCouponApplier) RedeemForInvoice(_ context.Context, _ string, req domain.CouponRedeemRequest) (domain.CouponRedeemResult, error) {
	m.redeemReq = req
	if m.redeemErr != nil {
		return domain.CouponRedeemResult{}, m.redeemErr
	}
	red := m.redeemResult
	if red.Redemption.DiscountCents == 0 {
		red.Redemption = domain.CouponRedemption{
			ID:             fmt.Sprintf("vlx_cpr_%d", len(m.markedRedemptions)+1),
			CustomerID:     req.CustomerID,
			SubscriptionID: req.SubscriptionID,
			InvoiceID:      req.InvoiceID,
			DiscountCents:  req.SubtotalCents / 10, // default 10% so tests that don't script it still exercise the path
		}
	}
	return red, nil
}

func (m *mockCouponApplier) VoidRedemptionsForInvoice(_ context.Context, _, invoiceID string) (int, error) {
	m.voidedInvoices = append(m.voidedInvoices, invoiceID)
	if m.voidErr != nil {
		return 0, m.voidErr
	}
	return 1, nil
}

// Customer-scope coupon fires when the subscription produced no discount —
// the Stripe-style fallback. The engine must populate invoice.DiscountCents
// and call MarkPeriodsApplied with the customer-scope redemption so the
// assignment's periods_applied increments and duration-limited coupons
// exhaust on schedule.
func TestRunCycle_CustomerScopedCouponFiresWhenSubscriptionScopeZero(t *testing.T) {
	engine, _, _, _, invoices := setupEngine()
	applier := &mockCouponApplier{
		subResult:      domain.CouponDiscountResult{},
		customerResult: domain.CouponDiscountResult{Cents: 500, RedemptionIDs: []string{"red_cust"}},
	}
	engine.SetCouponApplier(applier)

	count, errs := engine.RunCycle(context.Background(), 50)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if count != 1 {
		t.Fatalf("got %d invoices, want 1", count)
	}
	if !applier.subCalled {
		t.Error("ApplyToInvoice should be called first (subscription scope)")
	}
	if !applier.customerCalled {
		t.Error("ApplyToInvoiceForCustomer should be called as fallback")
	}
	if len(invoices.invoices) != 1 {
		t.Fatalf("got %d invoices, want 1", len(invoices.invoices))
	}
	if got := invoices.invoices[0].DiscountCents; got != 500 {
		t.Errorf("DiscountCents = %d, want 500 (from customer-scope fallback)", got)
	}
	if len(applier.markedRedemptions) != 0 {
		t.Errorf("MarkPeriodsApplied must not run for customer-scope discount, got %v", applier.markedRedemptions)
	}
	if len(applier.markedCustomerDiscounts) != 1 || applier.markedCustomerDiscounts[0] != "red_cust" {
		t.Errorf("MarkCustomerDiscountPeriodsApplied ids = %v, want [red_cust]", applier.markedCustomerDiscounts)
	}
}

// Precedence rule: when the subscription already has an active coupon that
// produces a discount, the customer-scope fallback must not run. Stripe
// treats subscription.discount and customer.discount as mutually exclusive
// on the same invoice — stacking the two would double-discount.
func TestRunCycle_SubscriptionCouponBeatsCustomerScope(t *testing.T) {
	engine, _, _, _, invoices := setupEngine()
	applier := &mockCouponApplier{
		subResult:      domain.CouponDiscountResult{Cents: 300, RedemptionIDs: []string{"red_sub"}},
		customerResult: domain.CouponDiscountResult{Cents: 500, RedemptionIDs: []string{"red_cust"}},
	}
	engine.SetCouponApplier(applier)

	if _, errs := engine.RunCycle(context.Background(), 50); len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if !applier.subCalled {
		t.Error("ApplyToInvoice should be called")
	}
	if applier.customerCalled {
		t.Error("ApplyToInvoiceForCustomer must NOT run when subscription-scope won")
	}
	if got := invoices.invoices[0].DiscountCents; got != 300 {
		t.Errorf("DiscountCents = %d, want 300 (subscription-scope)", got)
	}
	if len(applier.markedRedemptions) != 1 || applier.markedRedemptions[0] != "red_sub" {
		t.Errorf("MarkPeriodsApplied redemption ids = %v, want [red_sub]", applier.markedRedemptions)
	}
	if len(applier.markedCustomerDiscounts) != 0 {
		t.Errorf("MarkCustomerDiscountPeriodsApplied must not run for subscription-scope discount, got %v", applier.markedCustomerDiscounts)
	}
}

// TestRunCycle_NoPendingChangeNoAppliedEvent ensures the event is gated on an
// actual swap — a subscription billing on its existing plan with no pending
// change must not emit a spurious applied event.
func TestRunCycle_NoPendingChangeNoAppliedEvent(t *testing.T) {
	periodStart := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	subs := &mockSubs{
		subs: map[string]domain.Subscription{
			"sub_1": {
				ID: "sub_1", TenantID: "t1", CustomerID: "cus_1",
				Items: []domain.SubscriptionItem{{
					ID: "si_1", PlanID: "pln_old", Quantity: 1,
				}},
				Status: domain.SubscriptionActive, BillingTime: domain.BillingTimeCalendar,
				CurrentBillingPeriodStart: &periodStart, CurrentBillingPeriodEnd: &periodEnd,
				NextBillingAt: &periodEnd,
			},
		},
		cycleUpdated: make(map[string]bool),
	}
	pricing := &mockPricing{
		plans: map[string]domain.Plan{
			"pln_old": {ID: "pln_old", Name: "Old", Currency: "USD", BillingInterval: domain.BillingMonthly, BaseAmountCents: 1000},
		},
	}
	invoices := &mockInvoices{}
	usage := &mockUsage{totals: map[string]int64{}}
	dispatcher := &capturingEventDispatcher{}
	engine := NewEngine(subs, usage, pricing, invoices, nil, &mockSettings{}, nil, nil, nil)
	engine.SetEventDispatcher(dispatcher)

	if _, errs := engine.RunCycle(context.Background(), 50); len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	for _, ev := range dispatcher.events {
		if ev.eventType == domain.EventSubscriptionPendingChangeApplied {
			t.Fatalf("applied event fired without a pending change: %+v", ev)
		}
	}
}

// seedDraftInvoice inserts a minimal draft invoice + one line item into the
// mockInvoices store. Shared by the ApplyCouponToInvoice test cluster so each
// case focuses on its own gate/assertion rather than seed plumbing.
func seedDraftInvoice(m *mockInvoices, tenantID, invoiceID, customerID, subscriptionID string, subtotal int64) {
	m.invoices = append(m.invoices, domain.Invoice{
		ID:             invoiceID,
		TenantID:       tenantID,
		CustomerID:     customerID,
		SubscriptionID: subscriptionID,
		Status:         domain.InvoiceDraft,
		Currency:       "USD",
		SubtotalCents:  subtotal,
		AmountDueCents: subtotal,
	})
	m.lineItems = append(m.lineItems, domain.InvoiceLineItem{
		ID:               "li_1",
		TenantID:         tenantID,
		InvoiceID:        invoiceID,
		Description:      "Base fee",
		Quantity:         1,
		UnitAmountCents:  subtotal,
		AmountCents:      subtotal,
		TotalAmountCents: subtotal,
	})
}

// Happy path: draft invoice → coupon applied → subtotal held, discount set,
// line items repriced, and MarkPeriodsApplied fires exactly once with the
// new redemption id.
func TestApplyCouponToInvoice_HappyPath(t *testing.T) {
	engine, _, _, _, invoices := setupEngine()
	seedDraftInvoice(invoices, "t1", "inv_1", "cus_1", "sub_1", 10_000)

	applier := &mockCouponApplier{
		redeemResult: domain.CouponRedeemResult{
			Redemption: domain.CouponRedemption{ID: "cpr_1", DiscountCents: 2_000},
		},
	}
	engine.SetCouponApplier(applier)

	updated, err := engine.ApplyCouponToInvoice(context.Background(), "t1", "inv_1", "SAVE20", "idem-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated.DiscountCents != 2_000 {
		t.Errorf("DiscountCents = %d, want 2000", updated.DiscountCents)
	}
	if updated.SubtotalCents != 10_000 {
		t.Errorf("SubtotalCents = %d, want 10000 (must not mutate)", updated.SubtotalCents)
	}
	if updated.TotalAmountCents != 8_000 {
		t.Errorf("TotalAmountCents = %d, want 8000", updated.TotalAmountCents)
	}
	if applier.redeemReq.Code != "SAVE20" {
		t.Errorf("redeem code = %q, want SAVE20", applier.redeemReq.Code)
	}
	if applier.redeemReq.IdempotencyKey != "idem-1" {
		t.Errorf("redeem idempotency key = %q, want idem-1", applier.redeemReq.IdempotencyKey)
	}
	if applier.redeemReq.SubtotalCents != 10_000 {
		t.Errorf("redeem subtotal = %d, want 10000", applier.redeemReq.SubtotalCents)
	}
	// Subscription's plan set should flow through so the PlanIDs gate matches
	// any item's plan.
	if len(applier.redeemReq.PlanIDs) != 1 || applier.redeemReq.PlanIDs[0] != "pln_1" {
		t.Errorf("redeem plan ids = %v, want [pln_1]", applier.redeemReq.PlanIDs)
	}
	if len(applier.markedRedemptions) != 1 || applier.markedRedemptions[0] != "cpr_1" {
		t.Errorf("MarkPeriodsApplied ids = %v, want [cpr_1]", applier.markedRedemptions)
	}
	if len(applier.voidedInvoices) != 0 {
		t.Errorf("void should not fire on happy path, got %v", applier.voidedInvoices)
	}
}

// Finalized invoices (or any non-draft) must be rejected at the gate before
// any redemption commits. Catches regressions where a misconfigured route
// exposes this on a paid/voided invoice.
func TestApplyCouponToInvoice_RejectsNonDraft(t *testing.T) {
	engine, _, _, _, invoices := setupEngine()
	seedDraftInvoice(invoices, "t1", "inv_1", "cus_1", "sub_1", 10_000)
	invoices.invoices[0].Status = domain.InvoiceFinalized

	applier := &mockCouponApplier{}
	engine.SetCouponApplier(applier)

	_, err := engine.ApplyCouponToInvoice(context.Background(), "t1", "inv_1", "SAVE20", "")
	if err == nil {
		t.Fatal("expected error on finalized invoice, got nil")
	}
	if applier.redeemReq.Code != "" {
		t.Error("redeem must not fire when gate rejects")
	}
}

// Re-applying a coupon to an invoice that already carries a discount is a
// caller error — the operator flow should be "void the invoice and start
// over," not silently stack discounts.
func TestApplyCouponToInvoice_RejectsAlreadyDiscounted(t *testing.T) {
	engine, _, _, _, invoices := setupEngine()
	seedDraftInvoice(invoices, "t1", "inv_1", "cus_1", "sub_1", 10_000)
	invoices.invoices[0].DiscountCents = 500

	engine.SetCouponApplier(&mockCouponApplier{})

	_, err := engine.ApplyCouponToInvoice(context.Background(), "t1", "inv_1", "SAVE20", "")
	if err == nil {
		t.Fatal("expected error on already-discounted invoice, got nil")
	}
}

// Once the invoice has committed a Stripe tax_transaction we cannot recompute
// tax safely — must reject and let operators void/re-issue.
func TestApplyCouponToInvoice_RejectsTaxAlreadyCommitted(t *testing.T) {
	engine, _, _, _, invoices := setupEngine()
	seedDraftInvoice(invoices, "t1", "inv_1", "cus_1", "sub_1", 10_000)
	invoices.invoices[0].TaxTransactionID = "txr_123"

	engine.SetCouponApplier(&mockCouponApplier{})

	_, err := engine.ApplyCouponToInvoice(context.Background(), "t1", "inv_1", "SAVE20", "")
	if err == nil {
		t.Fatal("expected error when tax already committed, got nil")
	}
}

// Compensation: if the atomic discount persist fails after a fresh redeem,
// the redemption must be voided so times_redeemed stays honest.
func TestApplyCouponToInvoice_CompensatesOnPersistFailure(t *testing.T) {
	engine, _, _, _, invoices := setupEngine()
	seedDraftInvoice(invoices, "t1", "inv_1", "cus_1", "sub_1", 10_000)
	// Setting DiscountCents after the gate would normally be impossible, but
	// the memstore's ApplyDiscountAtomic rechecks it — so we flip the row
	// between gate and persist via a second pre-seeded invoice trick. Easier:
	// force the store to fail by pointing at a bogus invoice id that passes
	// the gate (via the first GetInvoice) but fails the atomic apply. The
	// simplest repro is to delete the line items before atomic runs. Use a
	// dedicated mock wrapper instead of hacking memstore.
	fm := &failingApplyInvoices{mockInvoices: invoices, failApply: true}
	// Swap the engine's invoice writer — the setup gave us the memstore
	// directly, so rewire.
	engine.invoices = fm

	applier := &mockCouponApplier{
		redeemResult: domain.CouponRedeemResult{
			Redemption: domain.CouponRedemption{ID: "cpr_1", DiscountCents: 2_000},
		},
	}
	engine.SetCouponApplier(applier)

	_, err := engine.ApplyCouponToInvoice(context.Background(), "t1", "inv_1", "SAVE20", "")
	if err == nil {
		t.Fatal("expected error when ApplyDiscountAtomic fails, got nil")
	}
	if len(applier.voidedInvoices) != 1 || applier.voidedInvoices[0] != "inv_1" {
		t.Errorf("voidedInvoices = %v, want [inv_1] (must compensate on failure)", applier.voidedInvoices)
	}
	if len(applier.markedRedemptions) != 0 {
		t.Errorf("MarkPeriodsApplied must not fire on failure path, got %v", applier.markedRedemptions)
	}
}

// Replay path: a repeated request with the same Idempotency-Key must not
// trigger the compensating void — the original call already persisted, so
// re-voiding would corrupt times_redeemed.
func TestApplyCouponToInvoice_ReplaySkipsCompensation(t *testing.T) {
	engine, _, _, _, invoices := setupEngine()
	seedDraftInvoice(invoices, "t1", "inv_1", "cus_1", "sub_1", 10_000)
	fm := &failingApplyInvoices{mockInvoices: invoices, failApply: true}
	engine.invoices = fm

	applier := &mockCouponApplier{
		redeemResult: domain.CouponRedeemResult{
			Redemption: domain.CouponRedemption{ID: "cpr_1", DiscountCents: 2_000},
			Replay:     true,
		},
	}
	engine.SetCouponApplier(applier)

	_, err := engine.ApplyCouponToInvoice(context.Background(), "t1", "inv_1", "SAVE20", "idem-dup")
	if err == nil {
		t.Fatal("expected error when ApplyDiscountAtomic fails, got nil")
	}
	if len(applier.voidedInvoices) != 0 {
		t.Errorf("replay path must NOT void — the first call owns the redemption. got %v", applier.voidedInvoices)
	}
}

// Defence-in-depth: a coupon that computes to zero discount is a bug — the
// redemption must be rolled back so the coupon's usage counter stays honest,
// and the caller gets a clear error instead of a no-op 200.
func TestApplyCouponToInvoice_ZeroDiscountVoidsAndErrors(t *testing.T) {
	engine, _, _, _, invoices := setupEngine()
	seedDraftInvoice(invoices, "t1", "inv_1", "cus_1", "sub_1", 10_000)

	applier := &mockCouponApplier{
		redeemResult: domain.CouponRedeemResult{
			Redemption: domain.CouponRedemption{ID: "cpr_1", DiscountCents: 0},
		},
	}
	// mockCouponApplier defaults a 10% discount when DiscountCents is 0 to
	// keep other tests concise, so route past that defaulting by letting
	// the service produce 0 via an explicit value-but-zero result.
	applier.redeemResult.Redemption.DiscountCents = 1
	applier.redeemResult.Redemption.ID = "cpr_zero"
	// Then override: the mock's "if DiscountCents == 0 default" only fires
	// on zero; we want a genuine zero-path, so swap to a purpose-built mock.
	zeroApplier := &zeroDiscountApplier{mockCouponApplier: applier}
	engine.SetCouponApplier(zeroApplier)

	_, err := engine.ApplyCouponToInvoice(context.Background(), "t1", "inv_1", "SAVE20", "")
	if err == nil {
		t.Fatal("expected error on zero-discount result, got nil")
	}
	if len(zeroApplier.voidedInvoices) != 1 {
		t.Errorf("zero-discount path must void the bogus redemption, got %v", zeroApplier.voidedInvoices)
	}
}

// failingApplyInvoices wraps mockInvoices and forces ApplyDiscountAtomic to
// fail while leaving all the other reads (GetInvoice, ListLineItems) working.
// Used by the compensation tests so the redeem → apply → void flow can fire
// the compensating branch without fighting the memstore's gate rechecks.
type failingApplyInvoices struct {
	*mockInvoices
	failApply bool
}

func (f *failingApplyInvoices) ApplyDiscountAtomic(ctx context.Context, tenantID, invoiceID string, update domain.InvoiceDiscountUpdate, items []domain.InvoiceLineItem) (domain.Invoice, error) {
	if f.failApply {
		return domain.Invoice{}, fmt.Errorf("simulated db failure")
	}
	return f.mockInvoices.ApplyDiscountAtomic(ctx, tenantID, invoiceID, update, items)
}

// zeroDiscountApplier forces RedeemForInvoice to return DiscountCents=0 so the
// engine's defence-in-depth branch can be exercised. The base mock applies a
// 10% default for tests that don't script a value, which hides the zero path.
type zeroDiscountApplier struct{ *mockCouponApplier }

func (z *zeroDiscountApplier) RedeemForInvoice(_ context.Context, _ string, req domain.CouponRedeemRequest) (domain.CouponRedeemResult, error) {
	z.redeemReq = req
	return domain.CouponRedeemResult{
		Redemption: domain.CouponRedemption{ID: "cpr_zero", DiscountCents: 0},
	}, nil
}

// TestRunCycle_CancelAtPeriodEnd_FiresAtBoundary locks in the schedule-cancel
// behaviour: when a sub has cancel_at_period_end=true and the cycle scan
// observes effectiveNow >= period end, the engine generates the final invoice
// for the just-ended period AND transitions status to canceled — instead of
// rolling next_billing_at to a future date that would never be reached.
func TestRunCycle_CancelAtPeriodEnd_FiresAtBoundary(t *testing.T) {
	periodStart := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	subs := &mockSubs{
		subs: map[string]domain.Subscription{
			"sub_1": {
				ID: "sub_1", TenantID: "t1", CustomerID: "cus_1",
				Items:                     []domain.SubscriptionItem{{PlanID: "pln_1", Quantity: 1}},
				Status:                    domain.SubscriptionActive,
				BillingTime:               domain.BillingTimeCalendar,
				CurrentBillingPeriodStart: &periodStart,
				CurrentBillingPeriodEnd:   &periodEnd,
				NextBillingAt:             &periodEnd,
				CancelAtPeriodEnd:         true,
			},
		},
		cycleUpdated: make(map[string]bool),
	}
	pricing := &mockPricing{
		plans: map[string]domain.Plan{
			"pln_1": {
				ID: "pln_1", Name: "Pro", Currency: "USD",
				BillingInterval: domain.BillingMonthly,
				BaseAmountCents: 4900,
			},
		},
	}
	invoices := &mockInvoices{}
	dispatcher := &capturingEventDispatcher{}
	engine := NewEngine(subs, &mockUsage{totals: map[string]int64{}}, pricing, invoices, nil, &mockSettings{}, nil, nil, nil)
	engine.SetEventDispatcher(dispatcher)

	if _, errs := engine.RunCycle(context.Background(), 50); len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	if len(invoices.invoices) != 1 {
		t.Fatalf("got %d invoices, want 1 (final invoice for just-ended period)", len(invoices.invoices))
	}

	updated := subs.subs["sub_1"]
	if updated.Status != domain.SubscriptionCanceled {
		t.Errorf("status: got %q, want canceled", updated.Status)
	}
	if updated.CanceledAt == nil {
		t.Error("canceled_at must be set after scheduled cancel fires")
	}
	if updated.CancelAtPeriodEnd {
		t.Error("cancel_at_period_end must be cleared after firing")
	}
	if updated.NextBillingAt != nil {
		t.Error("next_billing_at must be nil on canceled sub")
	}

	var got string
	for _, ev := range dispatcher.events {
		if ev.eventType == domain.EventSubscriptionCanceled {
			got = ev.eventType
			if ev.payload["triggered_by"] != "schedule" {
				t.Errorf("triggered_by: got %v, want schedule", ev.payload["triggered_by"])
			}
			break
		}
	}
	if got == "" {
		types := make([]string, 0, len(dispatcher.events))
		for _, ev := range dispatcher.events {
			types = append(types, ev.eventType)
		}
		t.Fatalf("expected %s, got types=%v", domain.EventSubscriptionCanceled, types)
	}
}

// TestRunCycle_CancelAt_FiresWhenTimestampReached covers the timestamp-based
// schedule. Setting cancel_at to the period boundary should fire the cancel
// at the same point cancel_at_period_end would, but via the timestamp path.
func TestRunCycle_CancelAt_FiresWhenTimestampReached(t *testing.T) {
	periodStart := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	cancelAt := periodEnd

	subs := &mockSubs{
		subs: map[string]domain.Subscription{
			"sub_1": {
				ID: "sub_1", TenantID: "t1", CustomerID: "cus_1",
				Items:                     []domain.SubscriptionItem{{PlanID: "pln_1", Quantity: 1}},
				Status:                    domain.SubscriptionActive,
				BillingTime:               domain.BillingTimeCalendar,
				CurrentBillingPeriodStart: &periodStart,
				CurrentBillingPeriodEnd:   &periodEnd,
				NextBillingAt:             &periodEnd,
				CancelAt:                  &cancelAt,
			},
		},
		cycleUpdated: make(map[string]bool),
	}
	pricing := &mockPricing{
		plans: map[string]domain.Plan{
			"pln_1": {
				ID: "pln_1", Name: "Pro", Currency: "USD",
				BillingInterval: domain.BillingMonthly,
				BaseAmountCents: 4900,
			},
		},
	}
	invoices := &mockInvoices{}
	engine := NewEngine(subs, &mockUsage{totals: map[string]int64{}}, pricing, invoices, nil, &mockSettings{}, nil, nil, nil)

	if _, errs := engine.RunCycle(context.Background(), 50); len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	updated := subs.subs["sub_1"]
	if updated.Status != domain.SubscriptionCanceled {
		t.Errorf("status: got %q, want canceled", updated.Status)
	}
	if updated.CancelAt != nil {
		t.Error("cancel_at must be cleared after firing")
	}
}

// TestShouldFireScheduledCancel covers the predicate's edge cases directly.
// Boundary equality matters: the period-end transition must fire when
// effectiveNow equals period_end, not just strictly past it, otherwise a sub
// that gets billed at the exact boundary tick would skip its cancel.
func TestShouldFireScheduledCancel(t *testing.T) {
	t0 := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		sub       domain.Subscription
		periodEnd time.Time
		now       time.Time
		want      bool
	}{
		{
			name:      "no schedule, never fires",
			sub:       domain.Subscription{},
			periodEnd: t0,
			now:       t0,
			want:      false,
		},
		{
			name:      "at_period_end, before boundary",
			sub:       domain.Subscription{CancelAtPeriodEnd: true},
			periodEnd: t0,
			now:       t0.Add(-time.Second),
			want:      false,
		},
		{
			name:      "at_period_end, exactly at boundary",
			sub:       domain.Subscription{CancelAtPeriodEnd: true},
			periodEnd: t0,
			now:       t0,
			want:      true,
		},
		{
			name:      "at_period_end, past boundary",
			sub:       domain.Subscription{CancelAtPeriodEnd: true},
			periodEnd: t0,
			now:       t0.Add(time.Hour),
			want:      true,
		},
		{
			name:      "cancel_at, before timestamp",
			sub:       domain.Subscription{CancelAt: &t0},
			periodEnd: t0.Add(time.Hour),
			now:       t0.Add(-time.Second),
			want:      false,
		},
		{
			name:      "cancel_at, exactly at timestamp",
			sub:       domain.Subscription{CancelAt: &t0},
			periodEnd: t0.Add(time.Hour),
			now:       t0,
			want:      true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldFireScheduledCancel(tt.sub, tt.periodEnd, tt.now); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// TestRunCycle_PauseCollection_GeneratesDraft locks in the Stripe-parity
// behavior: a sub with pause_collection set still has its cycle advanced and
// an invoice generated, but the invoice is created as draft (which keeps it
// out of finalize/charge/dunning). Distinct from a hard pause
// (status=paused), which excludes the sub from GetDueBilling — the sub here
// is still active.
func TestRunCycle_PauseCollection_GeneratesDraft(t *testing.T) {
	periodStart := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	subs := &mockSubs{
		subs: map[string]domain.Subscription{
			"sub_1": {
				ID: "sub_1", TenantID: "t1", CustomerID: "cus_1",
				Items:                     []domain.SubscriptionItem{{PlanID: "pln_1", Quantity: 1}},
				Status:                    domain.SubscriptionActive,
				BillingTime:               domain.BillingTimeCalendar,
				CurrentBillingPeriodStart: &periodStart,
				CurrentBillingPeriodEnd:   &periodEnd,
				NextBillingAt:             &periodEnd,
				PauseCollection: &domain.PauseCollection{
					Behavior: domain.PauseCollectionKeepAsDraft,
				},
			},
		},
		cycleUpdated: make(map[string]bool),
	}
	pricing := &mockPricing{
		plans: map[string]domain.Plan{
			"pln_1": {
				ID: "pln_1", Name: "Pro", Currency: "USD",
				BillingInterval: domain.BillingMonthly,
				BaseAmountCents: 4900,
			},
		},
	}
	invoices := &mockInvoices{}
	engine := NewEngine(subs, &mockUsage{totals: map[string]int64{}}, pricing, invoices, nil, &mockSettings{}, nil, nil, nil)

	if _, errs := engine.RunCycle(context.Background(), 50); len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	if len(invoices.invoices) != 1 {
		t.Fatalf("got %d invoices, want 1 (cycle should still emit invoice during pause_collection)", len(invoices.invoices))
	}
	got := invoices.invoices[0]
	if got.Status != domain.InvoiceDraft {
		t.Errorf("invoice status: got %q, want %q (pause_collection forces draft)", got.Status, domain.InvoiceDraft)
	}
	if !subs.cycleUpdated["sub_1"] {
		t.Error("billing cycle should still advance during pause_collection")
	}

	updated := subs.subs["sub_1"]
	if updated.PauseCollection == nil {
		t.Error("PauseCollection must remain set when no resumes_at is configured")
	}
}

// TestRunCycle_PauseCollection_AutoResumesWhenResumesAtPasses verifies the
// cycle scan auto-clears pause_collection when resumes_at <= effectiveNow.
// After the clear, the rest of the billing run treats the sub as fully
// resumed: the invoice is finalized (not draft), and a
// subscription.collection_resumed event fires with triggered_by="schedule"
// so analytics can distinguish auto-resume from operator-triggered resume.
func TestRunCycle_PauseCollection_AutoResumesWhenResumesAtPasses(t *testing.T) {
	periodStart := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	resumesAt := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC) // earlier than periodEnd (= "now")

	subs := &mockSubs{
		subs: map[string]domain.Subscription{
			"sub_1": {
				ID: "sub_1", TenantID: "t1", CustomerID: "cus_1",
				Items:                     []domain.SubscriptionItem{{PlanID: "pln_1", Quantity: 1}},
				Status:                    domain.SubscriptionActive,
				BillingTime:               domain.BillingTimeCalendar,
				CurrentBillingPeriodStart: &periodStart,
				CurrentBillingPeriodEnd:   &periodEnd,
				NextBillingAt:             &periodEnd,
				PauseCollection: &domain.PauseCollection{
					Behavior:  domain.PauseCollectionKeepAsDraft,
					ResumesAt: &resumesAt,
				},
			},
		},
		cycleUpdated: make(map[string]bool),
	}
	pricing := &mockPricing{
		plans: map[string]domain.Plan{
			"pln_1": {
				ID: "pln_1", Name: "Pro", Currency: "USD",
				BillingInterval: domain.BillingMonthly,
				BaseAmountCents: 4900,
			},
		},
	}
	invoices := &mockInvoices{}
	dispatcher := &capturingEventDispatcher{}
	engine := NewEngine(subs, &mockUsage{totals: map[string]int64{}}, pricing, invoices, nil, &mockSettings{}, nil, nil, nil)
	engine.SetEventDispatcher(dispatcher)

	if _, errs := engine.RunCycle(context.Background(), 50); len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	if len(invoices.invoices) != 1 {
		t.Fatalf("got %d invoices, want 1", len(invoices.invoices))
	}
	got := invoices.invoices[0]
	if got.Status != domain.InvoiceFinalized {
		t.Errorf("invoice status after auto-resume: got %q, want %q (pause cleared, treat as normal)", got.Status, domain.InvoiceFinalized)
	}

	updated := subs.subs["sub_1"]
	if updated.PauseCollection != nil {
		t.Errorf("PauseCollection should be cleared after auto-resume, got %+v", updated.PauseCollection)
	}

	var resumeEvent map[string]any
	for _, ev := range dispatcher.events {
		if ev.eventType == domain.EventSubscriptionCollectionResumed {
			resumeEvent = ev.payload
			break
		}
	}
	if resumeEvent == nil {
		types := make([]string, 0, len(dispatcher.events))
		for _, ev := range dispatcher.events {
			types = append(types, ev.eventType)
		}
		t.Fatalf("expected %s event, got types=%v", domain.EventSubscriptionCollectionResumed, types)
	}
	if resumeEvent["triggered_by"] != "schedule" {
		t.Errorf("triggered_by: got %v, want schedule", resumeEvent["triggered_by"])
	}
}

// TestRunCycle_Trial_Active_SkipsBillingAndAdvancesCycle covers case (a) of
// the trial state machine: the cycle scan visits a trialing sub whose trial
// has not yet elapsed. No invoice generated; next_billing_at advances.
func TestRunCycle_Trial_Active_SkipsBillingAndAdvancesCycle(t *testing.T) {
	periodStart := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC) // past, so cycle scan picks it up
	trialEnd := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)  // far-future: trial still active at scan time

	subs := &mockSubs{
		subs: map[string]domain.Subscription{
			"sub_1": {
				ID: "sub_1", TenantID: "t1", CustomerID: "cus_1",
				Items:                     []domain.SubscriptionItem{{PlanID: "pln_1", Quantity: 1}},
				Status:                    domain.SubscriptionTrialing,
				BillingTime:               domain.BillingTimeCalendar,
				TrialEndAt:                &trialEnd,
				CurrentBillingPeriodStart: &periodStart,
				CurrentBillingPeriodEnd:   &periodEnd,
				NextBillingAt:             &periodEnd,
			},
		},
		cycleUpdated: make(map[string]bool),
	}
	pricing := &mockPricing{
		plans: map[string]domain.Plan{
			"pln_1": {ID: "pln_1", Currency: "USD", BillingInterval: domain.BillingMonthly, BaseAmountCents: 4900},
		},
	}
	invoices := &mockInvoices{}
	dispatcher := &capturingEventDispatcher{}
	engine := NewEngine(subs, &mockUsage{totals: map[string]int64{}}, pricing, invoices, nil, &mockSettings{}, nil, nil, nil)
	engine.SetEventDispatcher(dispatcher)

	if _, errs := engine.RunCycle(context.Background(), 50); len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	if len(invoices.invoices) != 0 {
		t.Errorf("expected 0 invoices during active trial, got %d", len(invoices.invoices))
	}
	if !subs.cycleUpdated["sub_1"] {
		t.Error("expected cycle to be advanced even when trial-skipping")
	}
	if subs.subs["sub_1"].Status != domain.SubscriptionTrialing {
		t.Errorf("status should remain trialing during active trial, got %q", subs.subs["sub_1"].Status)
	}
}

// TestRunCycle_Trial_Ended_AutoActivatesAndBills covers case (b): cycle scan
// arrives after trial_end_at; the engine flips status to active, fires
// subscription.trial_ended (triggered_by="schedule"), then bills the period
// normally.
func TestRunCycle_Trial_Ended_AutoActivatesAndBills(t *testing.T) {
	periodStart := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC) // past, so cycle scan picks it up
	trialEnd := time.Date(2026, 2, 15, 0, 0, 0, 0, time.UTC) // past: trial elapsed before now

	subs := &mockSubs{
		subs: map[string]domain.Subscription{
			"sub_1": {
				ID: "sub_1", TenantID: "t1", CustomerID: "cus_1",
				Items:                     []domain.SubscriptionItem{{PlanID: "pln_1", Quantity: 1}},
				Status:                    domain.SubscriptionTrialing,
				BillingTime:               domain.BillingTimeCalendar,
				TrialEndAt:                &trialEnd,
				CurrentBillingPeriodStart: &periodStart,
				CurrentBillingPeriodEnd:   &periodEnd,
				NextBillingAt:             &periodEnd,
			},
		},
		cycleUpdated: make(map[string]bool),
	}
	pricing := &mockPricing{
		plans: map[string]domain.Plan{
			"pln_1": {ID: "pln_1", Currency: "USD", BillingInterval: domain.BillingMonthly, BaseAmountCents: 4900},
		},
	}
	invoices := &mockInvoices{}
	dispatcher := &capturingEventDispatcher{}
	engine := NewEngine(subs, &mockUsage{totals: map[string]int64{}}, pricing, invoices, nil, &mockSettings{}, nil, nil, nil)
	engine.SetEventDispatcher(dispatcher)

	if _, errs := engine.RunCycle(context.Background(), 50); len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	if len(invoices.invoices) != 1 {
		t.Fatalf("expected 1 invoice after trial ends, got %d", len(invoices.invoices))
	}
	if invoices.invoices[0].Status != domain.InvoiceFinalized {
		t.Errorf("invoice status: got %q, want finalized", invoices.invoices[0].Status)
	}

	updated := subs.subs["sub_1"]
	if updated.Status != domain.SubscriptionActive {
		t.Errorf("status: got %q, want active after trial ends", updated.Status)
	}
	if updated.ActivatedAt == nil {
		t.Error("activated_at should be stamped after trial-end auto-flip")
	}

	var trialEndedEvent map[string]any
	for _, ev := range dispatcher.events {
		if ev.eventType == domain.EventSubscriptionTrialEnded {
			trialEndedEvent = ev.payload
			break
		}
	}
	if trialEndedEvent == nil {
		types := make([]string, 0, len(dispatcher.events))
		for _, ev := range dispatcher.events {
			types = append(types, ev.eventType)
		}
		t.Fatalf("expected %s event, got types=%v", domain.EventSubscriptionTrialEnded, types)
	}
	if trialEndedEvent["triggered_by"] != "schedule" {
		t.Errorf("triggered_by: got %v, want schedule", trialEndedEvent["triggered_by"])
	}
}
