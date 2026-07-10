package billing

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// cancelFailLoudFixture builds the minimal canceled sub used by both
// fail-loud tests: mid-period immediate cancel of an in_arrears plan.
func cancelFailLoudFixture() (domain.Subscription, *mockPricing, *mockUsage, *mockInvoices) {
	periodStart := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	cancelAt := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	sub := domain.Subscription{
		ID: "sub_1", TenantID: "t1", CustomerID: "cus_1", Code: "api",
		Status: domain.SubscriptionCanceled,
		Items: []domain.SubscriptionItem{{
			ID: "si_1", PlanID: "pln_api", Quantity: 1,
		}},
		CurrentBillingPeriodStart: &periodStart,
		CurrentBillingPeriodEnd:   &periodEnd,
		CanceledAt:                &cancelAt,
	}
	pricing := &mockPricing{
		plans: map[string]domain.Plan{
			"pln_api": {ID: "pln_api", Name: "API", Currency: "USD",
				BillingInterval: domain.BillingMonthly, BaseAmountCents: 3000,
				BaseBillTiming: domain.BillInArrears, MeterIDs: []string{"mtr_api"}},
		},
		meters: map[string]domain.Meter{
			"mtr_api": {ID: "mtr_api", Key: "api", Name: "API Calls",
				Unit: "calls", Aggregation: "sum", RatingRuleVersionID: "rrv_api"},
		},
		rules: map[string]domain.RatingRuleVersion{
			"rrv_api": {ID: "rrv_api", RuleKey: "api_calls", Version: 1,
				Mode: domain.PricingFlat, FlatAmountCents: decimal.NewFromInt(2)},
		},
	}
	return sub, pricing, &mockUsage{totals: map[string]int64{"mtr_api": 500}}, &mockInvoices{}
}

// TestBillFinalOnImmediateCancel_ChangeLogReadFailsLoud pins fail-loud parity
// with the cycle builder (2026-07-10 design review): pre-fix the change-log
// read error was swallowed (`itemChanges, _ :=`), so a transient DB failure
// silently billed a changed sub's FINAL invoice as one full-window line at the
// current plan's rate — mis-billing with no operator signal. The cancel bill
// must fail instead (the operator's cancel retries).
func TestBillFinalOnImmediateCancel_ChangeLogReadFailsLoud(t *testing.T) {
	sub, pricing, usage, invoices := cancelFailLoudFixture()
	subs := &mockSubs{itemChangesErr: errors.New("db blip")}
	engine := wireBaseTax(NewEngine(subs, usage, pricing, invoices, nil, &mockSettings{}, nil, nil, billingTestClock()))

	_, err := engine.BillFinalOnImmediateCancel(context.Background(), sub)
	if err == nil {
		t.Fatal("change-log read failure must fail the cancel bill, not silently single-line it")
	}
	if !strings.Contains(err.Error(), "list item changes on cancel") {
		t.Errorf("error should name the failed read, got %v", err)
	}
	if len(invoices.invoices) != 0 {
		t.Errorf("no invoice may be created on a failed change-log read, got %d", len(invoices.invoices))
	}
}

// TestBillFinalOnImmediateCancel_SegmentPlanLookupFailsLoud pins the second
// half: a change-log segment whose plan can't be hydrated was silently
// DROPPED (GetPlan err -> continue; the segment loop then skips unknown
// plans) — underbilling the final invoice with no signal. It must error.
func TestBillFinalOnImmediateCancel_SegmentPlanLookupFailsLoud(t *testing.T) {
	sub, pricing, usage, invoices := cancelFailLoudFixture()
	// A mid-period plan change whose from-plan is NOT in the pricing mock:
	// hydration hits mockPricing.GetPlan's "plan not found".
	subs := &mockSubs{itemChanges: []domain.SubscriptionItemChange{{
		SubscriptionID: "sub_1", SubscriptionItemID: "si_1", ChangeType: "plan",
		FromPlanID: "pln_gone", ToPlanID: "pln_api",
		ChangedAt: time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC),
	}}}
	engine := wireBaseTax(NewEngine(subs, usage, pricing, invoices, nil, &mockSettings{}, nil, nil, billingTestClock()))

	_, err := engine.BillFinalOnImmediateCancel(context.Background(), sub)
	if err == nil {
		t.Fatal("segment-plan lookup failure must fail the cancel bill, not silently drop the segment")
	}
	if !strings.Contains(err.Error(), "get segment plan pln_gone on cancel") {
		t.Errorf("error should name the missing segment plan, got %v", err)
	}
	if len(invoices.invoices) != 0 {
		t.Errorf("no invoice may be created on a failed segment hydration, got %d", len(invoices.invoices))
	}
}
