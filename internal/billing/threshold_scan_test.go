package billing

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// thresholdMockSubs extends mockSubs with a candidate filter so the
// scan-tick test can exercise the candidate-fetch path without coupling to
// mockSubs's status-based GetDueBilling filter.
type thresholdMockSubs struct {
	*mockSubs
	candidates []domain.Subscription
}

func (m *thresholdMockSubs) ListWithThresholds(_ context.Context, _ bool, _ int) ([]domain.Subscription, error) {
	return m.candidates, nil
}

// setupThresholdEngine builds a minimal engine wired with mocks that mirror the
// natural-cycle setupEngine but with a single-meter, single-rule plan so the
// running subtotal under test is unambiguous. Returns the engine, the wrapping
// mockSubs (for cycle-advance assertion), and the mock invoice store.
func setupThresholdEngine(thresholds *domain.BillingThresholds, usageQty int64) (*Engine, *thresholdMockSubs, *mockInvoices) {
	periodStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	nextBilling := periodEnd

	sub := domain.Subscription{
		ID: "sub_1", TenantID: "t1", CustomerID: "cus_1",
		Items: []domain.SubscriptionItem{
			{ID: "subitem_1", PlanID: "pln_1", Quantity: 1},
		},
		Status:                    domain.SubscriptionActive,
		BillingTime:               domain.BillingTimeCalendar,
		CurrentBillingPeriodStart: &periodStart,
		CurrentBillingPeriodEnd:   &periodEnd,
		NextBillingAt:             &nextBilling,
		BillingThresholds:         thresholds,
	}

	base := &mockSubs{
		subs:         map[string]domain.Subscription{"sub_1": sub},
		cycleUpdated: make(map[string]bool),
	}
	subs := &thresholdMockSubs{
		mockSubs:   base,
		candidates: []domain.Subscription{sub},
	}

	usage := &mockUsage{
		totals: map[string]int64{
			"mtr_api": usageQty,
		},
	}

	pricing := &mockPricing{
		plans: map[string]domain.Plan{
			"pln_1": {
				ID: "pln_1", Name: "Pro Plan", Currency: "USD",
				BillingInterval: domain.BillingMonthly,
				BaseAmountCents: 4900,
				MeterIDs:        []string{"mtr_api"},
			},
		},
		meters: map[string]domain.Meter{
			"mtr_api": {ID: "mtr_api", Name: "API Calls", Unit: "calls", RatingRuleVersionID: "rrv_api"},
		},
		rules: map[string]domain.RatingRuleVersion{
			"rrv_api": {
				ID: "rrv_api", RuleKey: "api_calls", Version: 1, Mode: domain.PricingFlat,
				FlatAmountCents: 100, // 1 cent / call (in basis-of-100 — 100 = $1)
			},
		},
	}

	invoices := &mockInvoices{}

	engine := NewEngine(subs, usage, pricing, invoices, nil, &mockSettings{}, nil, nil, nil)
	return engine, subs, invoices
}

// TestEvaluateThresholds_AmountCross verifies the running-subtotal check —
// usage * unit_amount + base_fee crossing AmountGTE flips CrossedAny and
// CrossedAmount. The minimum bar to ship; if this drifts the engine would
// either over- or under-fire.
func TestEvaluateThresholds_AmountCross(t *testing.T) {
	// Plan: $49 base + 1000 calls @ $1 each = $1049 (104900 cents).
	// Threshold: $1000 (100000 cents) → crossed.
	thresholds := &domain.BillingThresholds{
		AmountGTE:         100000,
		ResetBillingCycle: true,
	}
	engine, _, _ := setupThresholdEngine(thresholds, 1000)

	sub, _ := engine.subs.Get(context.Background(), "t1", "sub_1")
	periodStart := *sub.CurrentBillingPeriodStart
	now := periodStart.AddDate(0, 0, 5) // 5 days into the cycle

	eval, err := engine.evaluateThresholds(context.Background(), sub, periodStart, now)
	if err != nil {
		t.Fatalf("evaluateThresholds: %v", err)
	}

	if !eval.CrossedAny {
		t.Error("expected CrossedAny to be true")
	}
	if !eval.CrossedAmount {
		t.Error("expected CrossedAmount to be true")
	}
	if eval.CrossedItem {
		t.Error("CrossedItem should be false (no item caps configured)")
	}
	// $49 base (4900) + 1000 calls × 100 cents/call (100000) = 104900 cents
	wantSubtotal := int64(4900 + 1000*100)
	if eval.RunningSubtotal != wantSubtotal {
		t.Errorf("running subtotal: got %d, want %d", eval.RunningSubtotal, wantSubtotal)
	}
	if eval.InvoiceCurrency != "USD" {
		t.Errorf("currency: got %q, want USD", eval.InvoiceCurrency)
	}
	if len(eval.LineItems) == 0 {
		t.Error("expected non-empty line items")
	}
}

// TestEvaluateThresholds_BelowAmount asserts no fire when running subtotal
// is below the configured cap. The natural cycle would still bill at end —
// the threshold scan just stays out of the way.
func TestEvaluateThresholds_BelowAmount(t *testing.T) {
	// Plan: $49 base + 100 calls × 100 cents = 14900 cents. Threshold: 100000 → not crossed.
	thresholds := &domain.BillingThresholds{
		AmountGTE:         100000,
		ResetBillingCycle: true,
	}
	engine, _, _ := setupThresholdEngine(thresholds, 100)

	sub, _ := engine.subs.Get(context.Background(), "t1", "sub_1")
	periodStart := *sub.CurrentBillingPeriodStart
	now := periodStart.AddDate(0, 0, 5)

	eval, err := engine.evaluateThresholds(context.Background(), sub, periodStart, now)
	if err != nil {
		t.Fatalf("evaluateThresholds: %v", err)
	}

	if eval.CrossedAny {
		t.Error("expected CrossedAny to be false")
	}
	if eval.CrossedAmount {
		t.Error("expected CrossedAmount to be false")
	}
}

// TestEvaluateThresholds_ItemCross verifies the per-item quantity-cap path.
// Sums quantities across each item's plan meters during the partial cycle —
// any single item crossing fires.
func TestEvaluateThresholds_ItemCross(t *testing.T) {
	thresholds := &domain.BillingThresholds{
		ResetBillingCycle: true,
		ItemThresholds: []domain.SubscriptionItemThreshold{
			{SubscriptionItemID: "subitem_1", UsageGTE: decimal.NewFromInt(1000)},
		},
	}
	engine, _, _ := setupThresholdEngine(thresholds, 1500) // qty 1500 > cap 1000

	sub, _ := engine.subs.Get(context.Background(), "t1", "sub_1")
	periodStart := *sub.CurrentBillingPeriodStart
	now := periodStart.AddDate(0, 0, 5)

	eval, err := engine.evaluateThresholds(context.Background(), sub, periodStart, now)
	if err != nil {
		t.Fatalf("evaluateThresholds: %v", err)
	}

	if !eval.CrossedAny {
		t.Error("expected CrossedAny to be true (item crossed)")
	}
	if !eval.CrossedItem {
		t.Error("expected CrossedItem to be true")
	}
	if eval.CrossedItemID != "subitem_1" {
		t.Errorf("crossed item id: got %q, want subitem_1", eval.CrossedItemID)
	}
	if eval.CrossedAmount {
		t.Error("CrossedAmount should be false (no amount cap configured)")
	}
}

// TestEvaluateThresholds_BelowItemQuantity confirms the per-item path also
// respects the < cap case so we never spuriously fire.
func TestEvaluateThresholds_BelowItemQuantity(t *testing.T) {
	thresholds := &domain.BillingThresholds{
		ResetBillingCycle: true,
		ItemThresholds: []domain.SubscriptionItemThreshold{
			{SubscriptionItemID: "subitem_1", UsageGTE: decimal.NewFromInt(1000)},
		},
	}
	engine, _, _ := setupThresholdEngine(thresholds, 500)

	sub, _ := engine.subs.Get(context.Background(), "t1", "sub_1")
	periodStart := *sub.CurrentBillingPeriodStart
	now := periodStart.AddDate(0, 0, 5)

	eval, err := engine.evaluateThresholds(context.Background(), sub, periodStart, now)
	if err != nil {
		t.Fatalf("evaluateThresholds: %v", err)
	}

	if eval.CrossedAny {
		t.Error("expected CrossedAny to be false")
	}
}

// TestScanThresholds_SkipsTerminalSubs covers the gate that prevents
// firing on canceled/archived subs. The candidate query in postgres
// already restricts to active+trialing — this test guards the second-line
// defense in scanOneThreshold so a stale candidate row can't bypass.
func TestScanThresholds_SkipsTerminalSubs(t *testing.T) {
	thresholds := &domain.BillingThresholds{
		AmountGTE:         100, // very low cap so we'd cross if it fired
		ResetBillingCycle: true,
	}
	engine, subs, invoices := setupThresholdEngine(thresholds, 1000)

	// Force-cancel the candidate after build.
	s := subs.subs["sub_1"]
	s.Status = domain.SubscriptionCanceled
	subs.subs["sub_1"] = s
	subs.candidates[0] = s

	fired, errs := engine.ScanThresholds(context.Background(), 50)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if fired != 0 {
		t.Errorf("expected 0 fires for canceled sub, got %d", fired)
	}
	if len(invoices.invoices) != 0 {
		t.Errorf("expected 0 invoices, got %d", len(invoices.invoices))
	}
}

// TestScanThresholds_SkipsPauseCollection covers pause_collection: the
// scan must not emit an invoice that can't be charged or dunned.
func TestScanThresholds_SkipsPauseCollection(t *testing.T) {
	thresholds := &domain.BillingThresholds{
		AmountGTE:         100,
		ResetBillingCycle: true,
	}
	engine, subs, invoices := setupThresholdEngine(thresholds, 1000)

	s := subs.subs["sub_1"]
	s.PauseCollection = &domain.PauseCollection{
		Behavior: domain.PauseCollectionKeepAsDraft,
	}
	subs.subs["sub_1"] = s
	subs.candidates[0] = s

	fired, errs := engine.ScanThresholds(context.Background(), 50)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if fired != 0 {
		t.Errorf("expected 0 fires for paused sub, got %d", fired)
	}
	if len(invoices.invoices) != 0 {
		t.Errorf("expected 0 invoices, got %d", len(invoices.invoices))
	}
}

// TestScanThresholds_NoCandidates handles the fast-path when no rows
// have thresholds configured. Returns (0, nil) without entering the loop.
func TestScanThresholds_NoCandidates(t *testing.T) {
	engine, subs, _ := setupThresholdEngine(&domain.BillingThresholds{AmountGTE: 100}, 100)
	subs.candidates = nil

	fired, errs := engine.ScanThresholds(context.Background(), 50)
	if len(errs) > 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
	if fired != 0 {
		t.Errorf("fired count: got %d, want 0", fired)
	}
}
