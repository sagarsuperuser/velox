package billing

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/tax"
)

// taxSettings is a stub SettingsReader that returns a fixed tax rate/name and
// NextInvoiceNumber() is unused by ApplyTaxToLineItems so it errors to catch
// any accidental call.
type taxSettings struct {
	rateBP int
	name   string
}

func (s *taxSettings) Get(_ context.Context, _ string) (domain.TenantSettings, error) {
	return domain.TenantSettings{TaxRateBP: s.rateBP, TaxName: s.name}, nil
}

func (s *taxSettings) NextInvoiceNumber(_ context.Context, _ string) (string, error) {
	return "", errors.New("NextInvoiceNumber should not be called in ApplyTaxToLineItems")
}

// taxProfiles returns a billing profile for configured customers; missing
// customer → error (matches real store behaviour).
type taxProfiles struct {
	profiles map[string]domain.CustomerBillingProfile
}

func (p *taxProfiles) GetBillingProfile(_ context.Context, _, customerID string) (domain.CustomerBillingProfile, error) {
	bp, ok := p.profiles[customerID]
	if !ok {
		return domain.CustomerBillingProfile{}, fmt.Errorf("not found")
	}
	return bp, nil
}

// stubCalculator returns a prebuilt tax.TaxResult regardless of input. err
// short-circuits before result is consulted.
type stubCalculator struct {
	result *tax.TaxResult
	err    error
}

func (c *stubCalculator) CalculateTax(_ context.Context, _ string, _ tax.CustomerAddress, _ []tax.LineItemInput) (*tax.TaxResult, error) {
	return c.result, c.err
}

func newTaxTestEngine(rateBP int, name string, profiles map[string]domain.CustomerBillingProfile) *Engine {
	e := &Engine{
		settings: &taxSettings{rateBP: rateBP, name: name},
	}
	if profiles != nil {
		e.profiles = &taxProfiles{profiles: profiles}
	}
	return e
}

func TestApplyTaxToLineItems_TenantRate(t *testing.T) {
	e := newTaxTestEngine(1850, "VAT", nil)
	lineItems := []domain.InvoiceLineItem{{AmountCents: 10000, Description: "base"}}

	r, err := e.ApplyTaxToLineItems(context.Background(), "t1", "cus_1", "USD", 10000, 0, lineItems)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.TaxAmountCents != 1850 {
		t.Errorf("got tax %d, want 1850", r.TaxAmountCents)
	}
	if r.TaxRateBP != 1850 || r.TaxName != "VAT" {
		t.Errorf("got rate=%d name=%q, want 1850 VAT", r.TaxRateBP, r.TaxName)
	}
	if lineItems[0].TaxAmountCents != 1850 {
		t.Errorf("line item tax = %d, want 1850", lineItems[0].TaxAmountCents)
	}
	if lineItems[0].TotalAmountCents != 11850 {
		t.Errorf("line item total = %d, want 11850", lineItems[0].TotalAmountCents)
	}
}

func TestApplyTaxToLineItems_CustomerOverrideWins(t *testing.T) {
	override := 2500 // 25%
	profiles := map[string]domain.CustomerBillingProfile{
		"cus_1": {
			CustomerID:        "cus_1",
			TaxOverrideRateBP: &override,
			Country:           "GB",
			TaxID:             "GB123456",
		},
	}
	e := newTaxTestEngine(1850, "VAT", profiles)
	lineItems := []domain.InvoiceLineItem{{AmountCents: 10000}}

	r, err := e.ApplyTaxToLineItems(context.Background(), "t1", "cus_1", "USD", 10000, 0, lineItems)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.TaxAmountCents != 2500 {
		t.Errorf("got tax %d, want 2500 (25%% override)", r.TaxAmountCents)
	}
	if r.TaxRateBP != 2500 {
		t.Errorf("got rate %d, want 2500", r.TaxRateBP)
	}
	if r.TaxCountry != "GB" || r.TaxID != "GB123456" {
		t.Errorf("got country=%q taxID=%q, want GB GB123456", r.TaxCountry, r.TaxID)
	}
}

func TestApplyTaxToLineItems_Exempt(t *testing.T) {
	profiles := map[string]domain.CustomerBillingProfile{
		"cus_1": {CustomerID: "cus_1", TaxExempt: true},
	}
	e := newTaxTestEngine(1850, "VAT", profiles)
	lineItems := []domain.InvoiceLineItem{{AmountCents: 10000}}

	r, err := e.ApplyTaxToLineItems(context.Background(), "t1", "cus_1", "USD", 10000, 0, lineItems)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.TaxAmountCents != 0 {
		t.Errorf("got tax %d, want 0 (exempt)", r.TaxAmountCents)
	}
	if lineItems[0].TaxAmountCents != 0 {
		t.Errorf("line item tax = %d, want 0", lineItems[0].TaxAmountCents)
	}
}

func TestApplyTaxToLineItems_ZeroSubtotal(t *testing.T) {
	e := newTaxTestEngine(1850, "VAT", nil)
	lineItems := []domain.InvoiceLineItem{{AmountCents: 0}}

	r, err := e.ApplyTaxToLineItems(context.Background(), "t1", "cus_1", "USD", 0, 0, lineItems)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.TaxAmountCents != 0 {
		t.Errorf("got tax %d, want 0 on zero subtotal", r.TaxAmountCents)
	}
}

func TestApplyTaxToLineItems_DiscountReducesTax(t *testing.T) {
	// 18.5% on $100 → $18.50; with $50 discount → 18.5% on $50 → $9.25 (925 cents).
	e := newTaxTestEngine(1850, "VAT", nil)
	lineItems := []domain.InvoiceLineItem{{AmountCents: 10000}}

	r, err := e.ApplyTaxToLineItems(context.Background(), "t1", "cus_1", "USD", 10000, 5000, lineItems)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.TaxAmountCents != 925 {
		t.Errorf("got tax %d, want 925 (18.5%% of $50 net)", r.TaxAmountCents)
	}
}

func TestApplyTaxToLineItems_ProportionalDiscountDistribution(t *testing.T) {
	// Two lines of $50 + $150 = $200. $20 discount → $180 net.
	// Line 1 net = 5000 - round(5000*2000/20000) = 5000 - 500 = 4500
	// Line 2 net = 15000 - round(15000*2000/20000) = 15000 - 1500 = 13500
	// Tax 10% on 18000 = 1800. Per-line: 450 + 1350 = 1800. ✓
	e := newTaxTestEngine(1000, "VAT", nil)
	lineItems := []domain.InvoiceLineItem{
		{AmountCents: 5000, Description: "small"},
		{AmountCents: 15000, Description: "large"},
	}

	r, err := e.ApplyTaxToLineItems(context.Background(), "t1", "cus_1", "USD", 20000, 2000, lineItems)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.TaxAmountCents != 1800 {
		t.Errorf("got tax %d, want 1800 (10%% of $180 net)", r.TaxAmountCents)
	}

	lineTaxSum := lineItems[0].TaxAmountCents + lineItems[1].TaxAmountCents
	if lineTaxSum != r.TaxAmountCents {
		t.Errorf("line-level tax sum %d != invoice-level tax %d", lineTaxSum, r.TaxAmountCents)
	}
}

func TestApplyTaxToLineItems_CalculatorPath(t *testing.T) {
	// Calculator returns a specific per-line result; engine must apply it to
	// line items and skip the inline math.
	calc := &stubCalculator{
		result: &tax.TaxResult{
			TotalTaxAmountCents: 2000,
			TaxRateBP:           2000,
			TaxName:             "GST",
			TaxCountry:          "AU",
			LineItemTaxes: []tax.LineItemTax{
				{Index: 0, TaxRateBP: 2000, TaxAmountCents: 2000},
			},
		},
	}
	e := newTaxTestEngine(1850, "VAT", nil)
	e.taxCalc = calc
	lineItems := []domain.InvoiceLineItem{{AmountCents: 10000}}

	r, err := e.ApplyTaxToLineItems(context.Background(), "t1", "cus_1", "USD", 10000, 0, lineItems)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.TaxAmountCents != 2000 || r.TaxName != "GST" || r.TaxCountry != "AU" {
		t.Errorf("got %+v, want 2000 GST AU", r)
	}
	if lineItems[0].TaxAmountCents != 2000 {
		t.Errorf("line item tax = %d, want 2000 (calculator)", lineItems[0].TaxAmountCents)
	}
}

func TestApplyTaxToLineItems_CalculatorErrorZeroTax(t *testing.T) {
	// Calculator errors → warn + fall through to zero tax (matches original
	// behaviour — we don't silently use tenant default when Stripe Tax fails).
	calc := &stubCalculator{err: errors.New("stripe down")}
	e := newTaxTestEngine(1850, "VAT", nil)
	e.taxCalc = calc
	lineItems := []domain.InvoiceLineItem{{AmountCents: 10000}}

	r, err := e.ApplyTaxToLineItems(context.Background(), "t1", "cus_1", "USD", 10000, 0, lineItems)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.TaxAmountCents != 0 {
		t.Errorf("got tax %d, want 0 on calculator error", r.TaxAmountCents)
	}
}

func TestApplyTaxToLineItems_RoundingReconciliation(t *testing.T) {
	// Three lines with a rate that produces per-line rounding drift; verify
	// the last line gets a ±1¢ correction so the sum still matches the
	// invoice-level tax.
	e := newTaxTestEngine(725, "VAT", nil)
	lineItems := []domain.InvoiceLineItem{
		{AmountCents: 333},
		{AmountCents: 333},
		{AmountCents: 334},
	}

	r, err := e.ApplyTaxToLineItems(context.Background(), "t1", "cus_1", "USD", 1000, 0, lineItems)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var sum int64
	for _, li := range lineItems {
		sum += li.TaxAmountCents
	}
	if sum != r.TaxAmountCents {
		t.Errorf("line tax sum %d != invoice tax %d after reconciliation", sum, r.TaxAmountCents)
	}
}
