package billing

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/tax"
)

// taxSettings returns a fixed TenantSettings so engine tests don't need a
// real store. NextInvoiceNumber is unused in the tax-apply path and errors
// loudly if something calls it by accident.
type taxSettings struct {
	provider  string
	rateBP    int64
	name      string
	inclusive bool
	taxCode   string
}

func (s *taxSettings) Get(_ context.Context, _ string) (domain.TenantSettings, error) {
	return domain.TenantSettings{
		TaxProvider:           s.provider,
		TaxRateBP:             s.rateBP,
		TaxName:               s.name,
		TaxInclusive:          s.inclusive,
		DefaultProductTaxCode: s.taxCode,
	}, nil
}

func (s *taxSettings) NextInvoiceNumber(_ context.Context, _ string) (string, error) {
	return "", errors.New("NextInvoiceNumber must not be called in tax-apply tests")
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

// resolverFunc adapts a function into a TaxProviderResolver.
type resolverFunc func(ctx context.Context, ts domain.TenantSettings) (tax.Provider, error)

func (f resolverFunc) Resolve(ctx context.Context, ts domain.TenantSettings) (tax.Provider, error) {
	return f(ctx, ts)
}

// manualResolver wires a ManualProvider with the settings' rate/name.
func manualResolver() TaxProviderResolver {
	return resolverFunc(func(_ context.Context, ts domain.TenantSettings) (tax.Provider, error) {
		return tax.NewManualProvider(ts.TaxRateBP, ts.TaxName), nil
	})
}

// stubProvider returns a prebuilt Result from Calculate. Used to assert the
// engine correctly maps provider output onto line items without coupling
// tests to ManualProvider's arithmetic.
type stubProvider struct {
	result *tax.Result
	err    error
}

func (*stubProvider) Name() string { return "stub" }
func (s *stubProvider) Calculate(_ context.Context, _ tax.Request) (*tax.Result, error) {
	return s.result, s.err
}
func (*stubProvider) Commit(_ context.Context, _, _ string) error { return nil }

func stubResolver(p tax.Provider) TaxProviderResolver {
	return resolverFunc(func(_ context.Context, _ domain.TenantSettings) (tax.Provider, error) {
		return p, nil
	})
}

func newManualEngine(rateBP int64, name string, profiles map[string]domain.CustomerBillingProfile) *Engine {
	e := &Engine{
		settings:     &taxSettings{provider: "manual", rateBP: rateBP, name: name},
		taxProviders: manualResolver(),
	}
	if profiles != nil {
		e.profiles = &taxProfiles{profiles: profiles}
	}
	return e
}

func newInclusiveManualEngine(rateBP int64, name string) *Engine {
	return &Engine{
		settings:     &taxSettings{provider: "manual", rateBP: rateBP, name: name, inclusive: true},
		taxProviders: manualResolver(),
	}
}

func TestApplyTaxToLineItems_ManualFlatRate(t *testing.T) {
	e := newManualEngine(1850, "VAT", nil)
	lineItems := []domain.InvoiceLineItem{{AmountCents: 10000, Description: "base", Quantity: 1}}

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
	if r.TaxProvider != "manual" {
		t.Errorf("got provider %q, want manual", r.TaxProvider)
	}
	if lineItems[0].TaxAmountCents != 1850 || lineItems[0].TotalAmountCents != 11850 {
		t.Errorf("line: tax=%d total=%d, want 1850 and 11850",
			lineItems[0].TaxAmountCents, lineItems[0].TotalAmountCents)
	}
}

func TestApplyTaxToLineItems_ExemptStatusZeroesTax(t *testing.T) {
	profiles := map[string]domain.CustomerBillingProfile{
		"cus_1": {CustomerID: "cus_1", TaxStatus: tax.StatusExempt, TaxExemptReason: "501(c)(3) nonprofit"},
	}
	e := newManualEngine(1850, "VAT", profiles)
	lineItems := []domain.InvoiceLineItem{{AmountCents: 10000, Quantity: 1}}

	r, err := e.ApplyTaxToLineItems(context.Background(), "t1", "cus_1", "USD", 10000, 0, lineItems)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.TaxAmountCents != 0 {
		t.Errorf("got tax %d, want 0 (exempt)", r.TaxAmountCents)
	}
	if r.TaxExemptReason != "501(c)(3) nonprofit" {
		t.Errorf("got exempt reason %q, want the billing profile's value", r.TaxExemptReason)
	}
	if r.TaxReverseCharge {
		t.Error("exempt must not set reverse-charge flag")
	}
	if lineItems[0].TaxAmountCents != 0 {
		t.Errorf("line tax = %d, want 0", lineItems[0].TaxAmountCents)
	}
}

func TestApplyTaxToLineItems_ReverseChargeStatus(t *testing.T) {
	profiles := map[string]domain.CustomerBillingProfile{
		"cus_1": {CustomerID: "cus_1", Country: "DE", TaxStatus: tax.StatusReverseCharge, TaxID: "DE123456789"},
	}
	e := newManualEngine(1850, "VAT", profiles)
	lineItems := []domain.InvoiceLineItem{{AmountCents: 10000, Quantity: 1}}

	r, err := e.ApplyTaxToLineItems(context.Background(), "t1", "cus_1", "USD", 10000, 0, lineItems)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.TaxAmountCents != 0 {
		t.Errorf("got tax %d, want 0 (reverse charge)", r.TaxAmountCents)
	}
	if !r.TaxReverseCharge {
		t.Error("reverse-charge flag must be set so the PDF legend renders")
	}
	if r.TaxExemptReason != "" {
		t.Errorf("reverse-charge must not carry exempt reason, got %q", r.TaxExemptReason)
	}
}

func TestApplyTaxToLineItems_ZeroSubtotal(t *testing.T) {
	e := newManualEngine(1850, "VAT", nil)
	lineItems := []domain.InvoiceLineItem{{AmountCents: 0}}

	r, err := e.ApplyTaxToLineItems(context.Background(), "t1", "cus_1", "USD", 0, 0, lineItems)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.TaxAmountCents != 0 {
		t.Errorf("got tax %d, want 0", r.TaxAmountCents)
	}
}

func TestApplyTaxToLineItems_DiscountReducesTax(t *testing.T) {
	// 18.5% of ($100 - $50) = $9.25 → 925 cents.
	e := newManualEngine(1850, "VAT", nil)
	lineItems := []domain.InvoiceLineItem{{AmountCents: 10000, Quantity: 1}}

	r, err := e.ApplyTaxToLineItems(context.Background(), "t1", "cus_1", "USD", 10000, 5000, lineItems)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.TaxAmountCents != 925 {
		t.Errorf("got tax %d, want 925", r.TaxAmountCents)
	}
}

func TestApplyTaxToLineItems_ProportionalDiscountDistribution(t *testing.T) {
	// $50 + $150 = $200 gross; $20 discount; 10% → $18 tax total.
	// Line sums must match.
	e := newManualEngine(1000, "VAT", nil)
	lineItems := []domain.InvoiceLineItem{
		{AmountCents: 5000, Description: "small", Quantity: 1},
		{AmountCents: 15000, Description: "large", Quantity: 1},
	}

	r, err := e.ApplyTaxToLineItems(context.Background(), "t1", "cus_1", "USD", 20000, 2000, lineItems)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.TaxAmountCents != 1800 {
		t.Errorf("got tax %d, want 1800", r.TaxAmountCents)
	}
	if lineItems[0].TaxAmountCents+lineItems[1].TaxAmountCents != r.TaxAmountCents {
		t.Errorf("line tax sum != invoice tax (%d + %d vs %d)",
			lineItems[0].TaxAmountCents, lineItems[1].TaxAmountCents, r.TaxAmountCents)
	}
}

func TestApplyTaxToLineItems_ProviderResultMapped(t *testing.T) {
	// A provider that returns jurisdictional breakdown data — verify the
	// engine stamps per-line Jurisdiction/TaxCode/TaxRateBP back onto line
	// items without extra arithmetic.
	provider := &stubProvider{result: &tax.Result{
		Provider:        "stripe_tax",
		CalculationID:   "taxcalc_test_123",
		TotalTaxCents:   2000,
		EffectiveRateBP: 2000,
		TaxName:         "GST",
		TaxCountry:      "AU",
		Lines: []tax.ResultLine{
			{Ref: "line_0", NetAmountCents: 10000, TaxAmountCents: 2000, TaxRateBP: 2000, TaxName: "GST", Jurisdiction: "AU", TaxCode: "txcd_10103001"},
		},
	}}
	e := &Engine{
		settings:     &taxSettings{provider: "stripe_tax", rateBP: 0},
		taxProviders: stubResolver(provider),
	}
	lineItems := []domain.InvoiceLineItem{{AmountCents: 10000, Quantity: 1}}

	r, err := e.ApplyTaxToLineItems(context.Background(), "t1", "cus_1", "USD", 10000, 0, lineItems)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.TaxAmountCents != 2000 || r.TaxName != "GST" || r.TaxCountry != "AU" {
		t.Errorf("got %+v, want 2000 GST AU", r)
	}
	if r.TaxProvider != "stub" || r.TaxCalculationID != "taxcalc_test_123" {
		t.Errorf("got provider=%q calcID=%q, want stub / taxcalc_test_123",
			r.TaxProvider, r.TaxCalculationID)
	}
	if lineItems[0].TaxJurisdiction != "AU" || lineItems[0].TaxCode != "txcd_10103001" {
		t.Errorf("line: juris=%q code=%q, want AU / txcd_10103001",
			lineItems[0].TaxJurisdiction, lineItems[0].TaxCode)
	}
}

func TestApplyTaxToLineItems_ProviderErrorZeroesTax(t *testing.T) {
	// Provider errors → warn and fall through to zero tax. Billing must not
	// block on a third-party outage.
	provider := &stubProvider{err: errors.New("stripe down")}
	e := &Engine{
		settings:     &taxSettings{provider: "stripe_tax"},
		taxProviders: stubResolver(provider),
	}
	lineItems := []domain.InvoiceLineItem{{AmountCents: 10000, Quantity: 1}}

	r, err := e.ApplyTaxToLineItems(context.Background(), "t1", "cus_1", "USD", 10000, 0, lineItems)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.TaxAmountCents != 0 {
		t.Errorf("got tax %d, want 0 on provider error", r.TaxAmountCents)
	}
	if lineItems[0].TotalAmountCents != 10000 {
		t.Errorf("line total = %d, want 10000 (falls through cleanly)", lineItems[0].TotalAmountCents)
	}
}

func TestApplyTaxToLineItems_NoneProvider(t *testing.T) {
	// tax_provider='none' short-circuits the whole pipeline to zero tax.
	e := &Engine{
		settings: &taxSettings{provider: "none"},
		taxProviders: resolverFunc(func(_ context.Context, _ domain.TenantSettings) (tax.Provider, error) {
			return tax.NewNoneProvider(), nil
		}),
	}
	lineItems := []domain.InvoiceLineItem{{AmountCents: 10000, Quantity: 1}}

	r, err := e.ApplyTaxToLineItems(context.Background(), "t1", "cus_1", "USD", 10000, 0, lineItems)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.TaxAmountCents != 0 {
		t.Errorf("got tax %d, want 0", r.TaxAmountCents)
	}
	if r.TaxProvider != "none" {
		t.Errorf("got provider %q, want none", r.TaxProvider)
	}
}

func TestApplyTaxToLineItems_Inclusive_Simple(t *testing.T) {
	// $118 gross at 18% inclusive → $100 net + $18 tax.
	e := newInclusiveManualEngine(1800, "GST")
	lineItems := []domain.InvoiceLineItem{{AmountCents: 11800, Quantity: 1}}

	r, err := e.ApplyTaxToLineItems(context.Background(), "t1", "cus_1", "USD", 11800, 0, lineItems)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.TaxAmountCents != 1800 {
		t.Errorf("got tax %d, want 1800", r.TaxAmountCents)
	}
	if r.SubtotalCents != 10000 {
		t.Errorf("got subtotal %d, want 10000 (net)", r.SubtotalCents)
	}
	if got := r.SubtotalCents - r.DiscountCents + r.TaxAmountCents; got != 11800 {
		t.Errorf("invariant: got %d, want 11800 (customer paid)", got)
	}
	if lineItems[0].AmountCents != 10000 {
		t.Errorf("line amount = %d, want 10000 (net)", lineItems[0].AmountCents)
	}
	if lineItems[0].TaxAmountCents != 1800 {
		t.Errorf("line tax = %d, want 1800", lineItems[0].TaxAmountCents)
	}
}

func TestApplyTaxToLineItems_Inclusive_ZeroRate(t *testing.T) {
	e := newInclusiveManualEngine(0, "")
	lineItems := []domain.InvoiceLineItem{{AmountCents: 10000, Quantity: 1}}

	r, err := e.ApplyTaxToLineItems(context.Background(), "t1", "cus_1", "USD", 10000, 500, lineItems)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.TaxAmountCents != 0 {
		t.Errorf("got tax %d, want 0", r.TaxAmountCents)
	}
	if r.SubtotalCents != 10000 || r.DiscountCents != 500 {
		t.Errorf("inputs should pass through: subtotal=%d discount=%d", r.SubtotalCents, r.DiscountCents)
	}
}

func TestApplyTaxToLineItems_RoundingReconciliation(t *testing.T) {
	// Three lines at 7.25% produce per-line rounding drift; last line
	// absorbs the residual so line-tax sums match the invoice-level total.
	e := newManualEngine(725, "VAT", nil)
	lineItems := []domain.InvoiceLineItem{
		{AmountCents: 333, Quantity: 1},
		{AmountCents: 333, Quantity: 1},
		{AmountCents: 334, Quantity: 1},
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
		t.Errorf("line tax sum %d != invoice tax %d", sum, r.TaxAmountCents)
	}
}

func TestApplyTaxToLineItems_NoProvidersSkipsTax(t *testing.T) {
	// Safety net: engine without a resolver must not panic or apply tax.
	e := &Engine{settings: &taxSettings{provider: "manual", rateBP: 1800}}
	lineItems := []domain.InvoiceLineItem{{AmountCents: 10000, Quantity: 1}}

	r, err := e.ApplyTaxToLineItems(context.Background(), "t1", "cus_1", "USD", 10000, 0, lineItems)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.TaxAmountCents != 0 {
		t.Errorf("got tax %d, want 0 without resolver", r.TaxAmountCents)
	}
}
