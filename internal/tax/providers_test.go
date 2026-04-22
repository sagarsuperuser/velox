package tax

import (
	"context"
	"testing"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// TestNoneProvider verifies the zero-tax backend: names itself, returns one
// zero-tax line per input, Commit is a silent no-op. Covers the contract the
// billing engine relies on so tenants on tax_provider='none' never hit an
// unexpected error path.
func TestNoneProvider(t *testing.T) {
	p := NewNoneProvider()
	if p.Name() != "none" {
		t.Fatalf("Name() = %q, want none", p.Name())
	}

	req := Request{LineItems: []RequestLine{
		{Ref: "line_0", AmountCents: 1000},
		{Ref: "line_1", AmountCents: 2500},
	}}
	res, err := p.Calculate(context.Background(), req)
	if err != nil {
		t.Fatalf("Calculate: unexpected error: %v", err)
	}
	if res.Provider != "none" {
		t.Errorf("Provider = %q, want none", res.Provider)
	}
	if res.TotalTaxCents != 0 {
		t.Errorf("TotalTaxCents = %d, want 0", res.TotalTaxCents)
	}
	if len(res.Lines) != 2 {
		t.Fatalf("Lines len = %d, want 2", len(res.Lines))
	}
	if res.Lines[0].NetAmountCents != 1000 || res.Lines[1].NetAmountCents != 2500 {
		t.Errorf("Lines net mismatch: %+v", res.Lines)
	}
	for i, l := range res.Lines {
		if l.TaxAmountCents != 0 {
			t.Errorf("line %d tax = %d, want 0", i, l.TaxAmountCents)
		}
	}
	if err := p.Commit(context.Background(), "inv_1", "calc_1"); err != nil {
		t.Errorf("Commit: unexpected error: %v", err)
	}
}

// TestManualProvider_Exclusive covers the default flat-rate exclusive path:
// tax added on top of the net subtotal, per-line rounding reconciled to the
// jurisdiction-level total. Also checks the Breakdown row the PDF relies on
// for the aggregate tax label.
func TestManualProvider_Exclusive(t *testing.T) {
	p := NewManualProvider(1800, "GST") // 18%

	req := Request{
		Currency: "INR",
		LineItems: []RequestLine{
			{Ref: "line_0", AmountCents: 10000},
			{Ref: "line_1", AmountCents: 5000},
		},
	}
	res, err := p.Calculate(context.Background(), req)
	if err != nil {
		t.Fatalf("Calculate: unexpected error: %v", err)
	}
	if res.Provider != "manual" {
		t.Errorf("Provider = %q, want manual", res.Provider)
	}
	wantTotal := int64(2700) // 18% of 15000
	if res.TotalTaxCents != wantTotal {
		t.Errorf("TotalTaxCents = %d, want %d", res.TotalTaxCents, wantTotal)
	}
	if res.EffectiveRateBP != 1800 {
		t.Errorf("EffectiveRateBP = %d, want 1800", res.EffectiveRateBP)
	}
	if res.TaxName != "GST" {
		t.Errorf("TaxName = %q, want GST", res.TaxName)
	}
	if len(res.Breakdowns) != 1 || res.Breakdowns[0].AmountCents != wantTotal {
		t.Errorf("Breakdowns = %+v", res.Breakdowns)
	}

	sumTax := int64(0)
	for _, l := range res.Lines {
		sumTax += l.TaxAmountCents
	}
	if sumTax != wantTotal {
		t.Errorf("sum(line tax) = %d, want %d (rounding reconciliation failed)", sumTax, wantTotal)
	}
}

// TestManualProvider_Inclusive verifies gross → net carve-out: the engine's
// subtotal invariant depends on sum(Net) + tax == sum(original gross).
func TestManualProvider_Inclusive(t *testing.T) {
	p := NewManualProvider(2000, "VAT") // 20%

	req := Request{
		TaxInclusive: true,
		LineItems:    []RequestLine{{Ref: "line_0", AmountCents: 12000}},
	}
	res, err := p.Calculate(context.Background(), req)
	if err != nil {
		t.Fatalf("Calculate: %v", err)
	}
	// 12000 gross at 20% inclusive → 10000 net + 2000 tax
	if res.TotalTaxCents != 2000 {
		t.Errorf("TotalTaxCents = %d, want 2000", res.TotalTaxCents)
	}
	if res.Lines[0].NetAmountCents != 10000 {
		t.Errorf("NetAmountCents = %d, want 10000", res.Lines[0].NetAmountCents)
	}
	if res.Lines[0].TaxAmountCents+res.Lines[0].NetAmountCents != 12000 {
		t.Errorf("net+tax != gross: %+v", res.Lines[0])
	}
}

// TestManualProvider_ZeroRate: when the tenant has tax_rate_bp=0 (e.g. no
// tax collected in their jurisdiction) every line gets zero tax and the
// provider returns empty breakdowns — the PDF then skips the tax row.
func TestManualProvider_ZeroRate(t *testing.T) {
	p := NewManualProvider(0, "")

	req := Request{LineItems: []RequestLine{{Ref: "line_0", AmountCents: 5000}}}
	res, err := p.Calculate(context.Background(), req)
	if err != nil {
		t.Fatalf("Calculate: %v", err)
	}
	if res.TotalTaxCents != 0 {
		t.Errorf("TotalTaxCents = %d, want 0", res.TotalTaxCents)
	}
	if len(res.Breakdowns) != 0 {
		t.Errorf("Breakdowns = %+v, want empty", res.Breakdowns)
	}
}

// TestManualProvider_Exempt: when the customer is exempt, Calculate returns
// a zeroed Result with Exempt=true and the reason propagated to the PDF
// audit snapshot. Reverse-charge takes the same path but sets ReverseCharge
// instead of Exempt so the PDF can render the legally required legend.
func TestManualProvider_ExemptStatuses(t *testing.T) {
	p := NewManualProvider(1800, "GST")

	t.Run("exempt", func(t *testing.T) {
		req := Request{
			CustomerStatus:       StatusExempt,
			CustomerExemptReason: "501(c)(3)",
			LineItems:            []RequestLine{{Ref: "line_0", AmountCents: 10000}},
		}
		res, err := p.Calculate(context.Background(), req)
		if err != nil {
			t.Fatalf("Calculate: %v", err)
		}
		if !res.Exempt {
			t.Error("Exempt flag not set")
		}
		if res.ReverseCharge {
			t.Error("ReverseCharge should be false for exempt status")
		}
		if res.TotalTaxCents != 0 {
			t.Errorf("TotalTaxCents = %d, want 0", res.TotalTaxCents)
		}
		if res.ExemptReason != "501(c)(3)" {
			t.Errorf("ExemptReason = %q, want 501(c)(3)", res.ExemptReason)
		}
	})

	t.Run("reverse_charge", func(t *testing.T) {
		req := Request{
			CustomerStatus: StatusReverseCharge,
			LineItems:      []RequestLine{{Ref: "line_0", AmountCents: 10000}},
		}
		res, err := p.Calculate(context.Background(), req)
		if err != nil {
			t.Fatalf("Calculate: %v", err)
		}
		if !res.ReverseCharge {
			t.Error("ReverseCharge flag not set")
		}
		if res.Exempt {
			t.Error("Exempt should be false for reverse_charge")
		}
		if res.TotalTaxCents != 0 {
			t.Errorf("TotalTaxCents = %d, want 0", res.TotalTaxCents)
		}
	})
}

// TestResolver_Routing verifies tenant settings → provider mapping. Stripe
// Tax falls back to manual when no Stripe clients are configured so a
// mis-seeded tenant setting doesn't take billing offline.
func TestResolver_Routing(t *testing.T) {
	t.Run("none", func(t *testing.T) {
		r := NewResolver(nil)
		p, err := r.Resolve(context.Background(), domain.TenantSettings{TaxProvider: "none"})
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if p.Name() != "none" {
			t.Errorf("Name = %q, want none", p.Name())
		}
	})

	t.Run("manual", func(t *testing.T) {
		r := NewResolver(nil)
		p, err := r.Resolve(context.Background(), domain.TenantSettings{
			TaxProvider: "manual", TaxRateBP: 1800, TaxName: "GST",
		})
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if p.Name() != "manual" {
			t.Errorf("Name = %q, want manual", p.Name())
		}
	})

	t.Run("empty_provider_defaults_to_none", func(t *testing.T) {
		r := NewResolver(nil)
		p, err := r.Resolve(context.Background(), domain.TenantSettings{})
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if p.Name() != "none" {
			t.Errorf("Name = %q, want none (safe default)", p.Name())
		}
	})

	t.Run("unknown_provider_defaults_to_none", func(t *testing.T) {
		// Defensive: if a new provider is added to the enum but the
		// resolver switch isn't updated, billing must not crash.
		r := NewResolver(nil)
		p, err := r.Resolve(context.Background(), domain.TenantSettings{TaxProvider: "avalara"})
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if p.Name() != "none" {
			t.Errorf("Name = %q, want none (unknown falls through)", p.Name())
		}
	})

	t.Run("stripe_tax_without_clients_falls_back_to_manual", func(t *testing.T) {
		// Local dev without Stripe credentials still needs billing to work.
		r := NewResolver(nil)
		p, err := r.Resolve(context.Background(), domain.TenantSettings{
			TaxProvider: "stripe_tax", TaxRateBP: 500, TaxName: "Sales Tax",
		})
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if p.Name() != "manual" {
			t.Errorf("Name = %q, want manual (fallback)", p.Name())
		}
	})
}
