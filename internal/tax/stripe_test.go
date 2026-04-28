package tax

import (
	"testing"

	"github.com/stripe/stripe-go/v82"
)

// TestMapResult_TaxabilityReason_Persisted is the regression test for issue #4.
// Stripe Tax returns a structured `taxability_reason` on every per-line
// `tax_breakdown` entry; the engine needs that string preserved so the PDF
// and dashboard can distinguish two zero-tax outcomes (reverse_charge vs
// not_collecting vs customer_exempt) that read identically on the totals row
// but require different legends. mapResult was previously dropping the
// per-line value entirely; this test pins the round-trip.
func TestMapResult_TaxabilityReason_Persisted(t *testing.T) {
	cases := []struct {
		name string
		// reason is the Stripe-canonical taxability_reason string applied to
		// the single line in the mocked calculation.
		reason stripe.TaxCalculationLineItemTaxBreakdownTaxabilityReason
		// taxAmount is the Stripe-side per-line tax (zero for the reverse
		// charge / customer-exempt cases — matches the issue's motivating
		// scenario where tax_amount=0 lines need different legends).
		taxAmount int64
	}{
		{"not_collecting (no merchant registration)", stripe.TaxCalculationLineItemTaxBreakdownTaxabilityReasonNotCollecting, 0},
		{"reverse_charge (EU B2B)", stripe.TaxCalculationLineItemTaxBreakdownTaxabilityReasonReverseCharge, 0},
		{"customer_exempt (exemption certificate)", stripe.TaxCalculationLineItemTaxBreakdownTaxabilityReasonCustomerExempt, 0},
		{"product_exempt (jurisdiction excludes category)", stripe.TaxCalculationLineItemTaxBreakdownTaxabilityReasonProductExempt, 0},
		{"standard_rated (default sales tax path)", stripe.TaxCalculationLineItemTaxBreakdownTaxabilityReasonStandardRated, 825},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := Request{
				LineItems: []RequestLine{{Ref: "line_0", AmountCents: 10000, Quantity: 1}},
			}
			calc := &stripe.TaxCalculation{
				ID:                 "taxcalc_test",
				TaxAmountExclusive: c.taxAmount,
				LineItems: &stripe.TaxCalculationLineItemList{
					Data: []*stripe.TaxCalculationLineItem{{
						Reference: "line_0",
						Amount:    10000,
						AmountTax: c.taxAmount,
						TaxBreakdown: []*stripe.TaxCalculationLineItemTaxBreakdown{{
							Amount:           c.taxAmount,
							TaxabilityReason: c.reason,
						}},
					}},
				},
			}

			p := &StripeTaxProvider{}
			res, err := p.mapResult(calc, req)
			if err != nil {
				t.Fatalf("mapResult: unexpected error: %v", err)
			}
			if len(res.Lines) != 1 {
				t.Fatalf("Lines len = %d, want 1", len(res.Lines))
			}
			if got, want := res.Lines[0].TaxabilityReason, string(c.reason); got != want {
				t.Errorf("TaxabilityReason = %q, want %q (Stripe-canonical reason must round-trip onto the line)", got, want)
			}
		})
	}
}

// TestMapResult_TaxabilityReason_EmptyWhenNoBreakdown verifies the per-line
// reason stays "" when Stripe doesn't supply a TaxBreakdown[0] — defensive
// guard so a sparse Stripe response doesn't write a zero-value enum onto the
// invoice line. Empty is the correct sentinel for "no signal" downstream.
func TestMapResult_TaxabilityReason_EmptyWhenNoBreakdown(t *testing.T) {
	req := Request{LineItems: []RequestLine{{Ref: "line_0", AmountCents: 5000, Quantity: 1}}}
	calc := &stripe.TaxCalculation{
		ID: "taxcalc_no_breakdown",
		LineItems: &stripe.TaxCalculationLineItemList{
			Data: []*stripe.TaxCalculationLineItem{{
				Reference: "line_0",
				Amount:    5000,
				AmountTax: 0,
				// TaxBreakdown intentionally empty.
			}},
		},
	}

	p := &StripeTaxProvider{}
	res, err := p.mapResult(calc, req)
	if err != nil {
		t.Fatalf("mapResult: unexpected error: %v", err)
	}
	if len(res.Lines) != 1 {
		t.Fatalf("Lines len = %d, want 1", len(res.Lines))
	}
	if got := res.Lines[0].TaxabilityReason; got != "" {
		t.Errorf("TaxabilityReason = %q, want empty (no TaxBreakdown[0] should leave the field unset)", got)
	}
}
