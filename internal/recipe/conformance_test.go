package recipe

import (
	"reflect"
	"testing"

	"github.com/sagarsuperuser/velox/internal/domain"
)

func recipePlanSpec() domain.RecipePlan {
	return domain.RecipePlan{
		Code: "ai_api_pro", Name: "AI API", Currency: "USD",
		BillingInterval: domain.BillingMonthly, BaseAmountCents: 0,
		MeterKeys: []string{"tokens"},
	}
}

// A plan matching the recipe's declared spec adopts cleanly; any
// money-affecting divergence is caught; cosmetic/operator-owned changes are not.
func TestPlanConformanceDiff(t *testing.T) {
	base := domain.Plan{
		Code: "ai_api_pro", Name: "AI API", Currency: "USD",
		BillingInterval: domain.BillingMonthly, BaseAmountCents: 0,
		BaseBillTiming: domain.BillInArrears, MeterIDs: []string{"mtr_tokens"},
		Status: domain.PlanActive,
	}
	wantMeters := []string{"mtr_tokens"}

	cases := []struct {
		name    string
		mutate  func(*domain.Plan)
		diverge bool
	}{
		{"exact match adopts", func(p *domain.Plan) {}, false},
		{"empty timing normalizes to in_arrears (no false diff)", func(p *domain.Plan) { p.BaseBillTiming = "" }, false},
		{"cosmetic name change adopts", func(p *domain.Plan) { p.Name = "Renamed" }, false},
		{"description change adopts", func(p *domain.Plan) { p.Description = "note" }, false},
		{"archived status adopts", func(p *domain.Plan) { p.Status = domain.PlanArchived }, false},
		{"tax_code change adopts (excluded, documented)", func(p *domain.Plan) { p.TaxCode = "txcd_x" }, false},
		{"in_advance timing diverges (the reported bug)", func(p *domain.Plan) { p.BaseBillTiming = domain.BillInAdvance }, true},
		{"base amount diverges", func(p *domain.Plan) { p.BaseAmountCents = 9900 }, true},
		{"currency diverges", func(p *domain.Plan) { p.Currency = "EUR" }, true},
		{"billing interval diverges", func(p *domain.Plan) { p.BillingInterval = domain.BillingYearly }, true},
		{"meter set diverges", func(p *domain.Plan) { p.MeterIDs = []string{"mtr_other"} }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := base
			p.MeterIDs = append([]string(nil), base.MeterIDs...) // don't alias base
			tc.mutate(&p)
			diffs := planConformanceDiff(p, recipePlanSpec(), wantMeters)
			if got := len(diffs) > 0; got != tc.diverge {
				t.Fatalf("planConformanceDiff diverge=%v (%v), want %v", got, diffs, tc.diverge)
			}
		})
	}
}

func TestMeterConformanceDiff(t *testing.T) {
	rm := domain.RecipeMeter{Key: "tokens", Name: "Tokens", Unit: "tokens", Aggregation: "sum"}
	if d := meterConformanceDiff(domain.Meter{Key: "tokens", Name: "Tokens", Unit: "tokens", Aggregation: "sum"}, rm); len(d) != 0 {
		t.Errorf("matching meter should adopt, got %v", d)
	}
	if d := meterConformanceDiff(domain.Meter{Key: "tokens", Name: "X", Unit: "tok", Aggregation: "sum"}, rm); len(d) != 0 {
		t.Errorf("cosmetic name/unit change should adopt, got %v", d)
	}
	if d := meterConformanceDiff(domain.Meter{Key: "tokens", Aggregation: "max"}, rm); len(d) == 0 {
		t.Error("divergent aggregation must be caught — an adopted meter with a different roll-up silently mis-bills")
	}
}

// Drift guard ([[feedback_enforce_invariant_after_bugclass]]): every
// domain.Plan field must be classified money-CHECKED or COSMETIC-EXCLUDED, so a
// future field added to Plan can't silently escape the conformance gate.
func TestPlanConformance_DriftGuard(t *testing.T) {
	checked := map[string]bool{
		"Currency": true, "BillingInterval": true, "BaseAmountCents": true,
		"BaseBillTiming": true, "MeterIDs": true,
	}
	excluded := map[string]bool{
		"ID": true, "TenantID": true, "Code": true, "Name": true,
		"Description": true, "Status": true, "TaxCode": true,
		"CreatedAt": true, "UpdatedAt": true,
	}
	tp := reflect.TypeOf(domain.Plan{})
	for i := 0; i < tp.NumField(); i++ {
		f := tp.Field(i).Name
		if !checked[f] && !excluded[f] {
			t.Errorf("domain.Plan field %q is neither money-CHECKED nor COSMETIC-EXCLUDED in the recipe conformance gate — classify it in planConformanceDiff (and here), or a new money field silently escapes the gate", f)
		}
	}
}
