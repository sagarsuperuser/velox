package billing

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/platform/clock"
)

// recordingAudit captures engine audit rows so the once-per-cycle loudness
// floor can be asserted.
type recordingAudit struct {
	rows []string // action names, in order
}

func (r *recordingAudit) Log(_ context.Context, _, action, _, _, _ string, _ map[string]any) error {
	r.rows = append(r.rows, action)
	return nil
}

// setupMaxLastEngine wires the threshold fixture with a mixed-mode meter: one
// sum bucket (rrv_api, 1000 calls @ $1 = 100000c) and one max bucket
// (rrv_max, peak 50 @ $1 = 5000c), plus the plan's 4900c in_arrears base.
// Bucket modes ride RuleAggregation.AggregationMode exactly as the real
// store's COALESCE(rule_mode, default) emits them.
func setupMaxLastEngine(thresholds *domain.BillingThresholds) (*Engine, *thresholdMockSubs, *mockInvoices) {
	engine, subs, invoices := setupThresholdEngine(thresholds, 1000)
	engine.clock = clock.NewFake(time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC))
	mp := engine.pricing.(*mockPricing)
	mp.rules["rrv_max"] = domain.RatingRuleVersion{
		ID: "rrv_max", RuleKey: "gpu_peak", Version: 1, Mode: domain.PricingFlat,
		FlatAmountCents: decimal.NewFromInt(100), Currency: "USD",
	}
	mu := engine.usage.(*mockUsage)
	mu.ruleAggs = map[string][]domain.RuleAggregation{
		"mtr_api": {
			{RatingRuleVersionID: "rrv_api", AggregationMode: domain.AggSum, Quantity: decimal.NewFromInt(1000)},
			{RatingRuleVersionID: "rrv_max", AggregationMode: domain.AggMax, Quantity: decimal.NewFromInt(50)},
		},
	}
	return engine, subs, invoices
}

// TestFireThreshold_ResetFalse_DropsMaxBucket_SubtotalSplit locks ADR-066 §4
// for reset=false: the max bucket's line is DROPPED from the threshold
// invoice (the cycle close bills it full-period exactly once), its amount
// still counts toward the cap, and the invoice header charges ONLY the
// rendered lines — no phantom money. Mutation seams: classify per meter
// instead of per bucket → the max line rides; feed RunningSubtotal to the
// invoice → subtotal 109900.
func TestFireThreshold_ResetFalse_DropsMaxBucket_SubtotalSplit(t *testing.T) {
	// capRunning = 4900 base + 100000 sum + 5000 max = 109900 ≥ 107000: the
	// cap crosses ONLY because the max bucket counts (sum-only is 104900).
	thresholds := &domain.BillingThresholds{AmountGTE: 107000, ResetBillingCycle: false}
	engine, _, invoices := setupMaxLastEngine(thresholds)

	fired, errs := engine.ScanThresholds(context.Background(), 50)
	if len(errs) != 0 {
		t.Fatalf("scan errors: %v", errs)
	}
	if fired != 1 {
		t.Fatalf("fired = %d, want 1 (max amount must count toward the cap under reset=false)", fired)
	}
	inv := invoices.invoices[0]
	// Header charges rendered lines only: base 4900 + sum 100000.
	if inv.SubtotalCents != 104900 || inv.TotalAmountCents != 104900 {
		t.Errorf("invoice subtotal/total = %d/%d, want 104900 (dropped max bucket must not be charged)", inv.SubtotalCents, inv.TotalAmountCents)
	}
	var lineSum int64
	for _, li := range invoices.lineItems {
		if li.RatingRuleVersionID == "rrv_max" {
			t.Errorf("max bucket line rode the reset=false threshold invoice: %+v", li)
		}
		lineSum += li.AmountCents
	}
	if lineSum != inv.SubtotalCents {
		t.Errorf("header subtotal %d != sum of rendered lines %d", inv.SubtotalCents, lineSum)
	}
}

// TestFireThreshold_ResetTrue_MaxRides_ButExcludedFromCap locks the
// asymmetry that kills the runaway refire loop: under reset=true the max
// bucket RIDES the invoice (the re-anchored stub never gets a close for this
// window) but does NOT count toward amount_gte — a steady peak
// re-materializes in every re-anchored window, so counting it would fire one
// invoice + card charge per scheduler tick.
func TestFireThreshold_ResetTrue_MaxRides_ButExcludedFromCap(t *testing.T) {
	// Same numbers: cap 107000 needs the max amount to cross. Under
	// reset=true the max is cap-excluded → 104900 + prorated-base delta <
	// 107000 → NO fire.
	thresholds := &domain.BillingThresholds{AmountGTE: 107000, ResetBillingCycle: true}
	engine, _, invoices := setupMaxLastEngine(thresholds)

	fired, errs := engine.ScanThresholds(context.Background(), 50)
	if len(errs) != 0 {
		t.Fatalf("scan errors: %v", errs)
	}
	if fired != 0 {
		t.Fatalf("fired = %d, want 0 (max must NOT count toward the cap under reset=true — runaway refire guard)", fired)
	}
	if len(invoices.invoices) != 0 {
		t.Fatalf("invoices = %d, want 0", len(invoices.invoices))
	}

	// Lower the cap so sum+base alone cross: the fire must CARRY the max line
	// (dropping it under reset=true bills it by nobody).
	thresholds2 := &domain.BillingThresholds{AmountGTE: 50000, ResetBillingCycle: true}
	engine2, _, invoices2 := setupMaxLastEngine(thresholds2)
	fired2, errs2 := engine2.ScanThresholds(context.Background(), 50)
	if len(errs2) != 0 {
		t.Fatalf("scan 2 errors: %v", errs2)
	}
	if fired2 != 1 {
		t.Fatalf("fired 2 = %d, want 1", fired2)
	}
	var sawMax bool
	var lineSum int64
	for _, li := range invoices2.lineItems {
		if li.RatingRuleVersionID == "rrv_max" {
			sawMax = true
		}
		lineSum += li.AmountCents
	}
	if !sawMax {
		t.Error("max bucket line missing from the reset=true threshold invoice (usage billed by NOBODY)")
	}
	if invoices2.invoices[0].SubtotalCents != lineSum {
		t.Errorf("header subtotal %d != sum of rendered lines %d", invoices2.invoices[0].SubtotalCents, lineSum)
	}
}

// TestScanThresholds_PureMaxCrossing_SkipsWithOneArtifact locks ADR-066 §4b:
// a pure-max sub whose committed spend crosses the cap emits NO invoice
// (empty billable set — cycle close bills the bucket), NO per-tick errors
// (the pre-fix currency bail errored every tick forever), consumes NO invoice
// numbers, and leaves exactly ONE audit artifact across repeated ticks.
func TestScanThresholds_PureMaxCrossing_SkipsWithOneArtifact(t *testing.T) {
	thresholds := &domain.BillingThresholds{AmountGTE: 1000, ResetBillingCycle: false}
	engine, _, invoices := setupMaxLastEngine(thresholds)
	// Pure max: strip the sum bucket and the base fee.
	mu := engine.usage.(*mockUsage)
	mu.ruleAggs["mtr_api"] = []domain.RuleAggregation{
		{RatingRuleVersionID: "rrv_max", AggregationMode: domain.AggMax, Quantity: decimal.NewFromInt(50)},
	}
	mp := engine.pricing.(*mockPricing)
	pln := mp.plans["pln_1"]
	pln.BaseAmountCents = 0
	mp.plans["pln_1"] = pln
	audit := &recordingAudit{}
	engine.SetAuditLogger(audit)

	for tick := 0; tick < 3; tick++ {
		fired, errs := engine.ScanThresholds(context.Background(), 50)
		if len(errs) != 0 {
			t.Fatalf("tick %d errors: %v (crossed-but-deferred must not error)", tick, errs)
		}
		if fired != 0 {
			t.Fatalf("tick %d fired = %d, want 0", tick, fired)
		}
	}
	if len(invoices.invoices) != 0 {
		t.Fatalf("invoices = %d, want 0", len(invoices.invoices))
	}
	deferred := 0
	for _, a := range audit.rows {
		if a == "subscription.threshold_deferred" {
			deferred++
		}
	}
	if deferred != 1 {
		t.Errorf("threshold_deferred audit rows = %d across 3 ticks, want exactly 1 (loudness floor, not tick-spam)", deferred)
	}
	// No invoice number consumed by the deferral path.
	ms := engine.settings.(*mockSettings)
	if ms.next != 0 {
		t.Errorf("deferral consumed %d invoice numbers, want 0 (guard must sit before NextInvoiceNumber)", ms.next)
	}
}
