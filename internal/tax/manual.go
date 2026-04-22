package tax

import (
	"context"

	"github.com/sagarsuperuser/velox/internal/platform/money"
)

// ManualProvider applies a single tenant-configured tax rate (basis points)
// uniformly across every line item. Deliberately simple: no jurisdiction
// lookup, no per-product tax codes, no cross-border auto zero-rating.
// Tenants who need jurisdictional accuracy pick stripe_tax.
//
// Inclusive vs exclusive: honoured via Request.TaxInclusive. In exclusive
// mode the line amount is net and tax is added on top; in inclusive mode
// the line amount is gross and tax is carved out so subtotal + tax = gross.
//
// Exempt / reverse-charge routing lives one layer up: Calculate honours
// req.CustomerStatus and returns a zeroed Result with the appropriate flag
// set, so the invoice PDF renders the right legend regardless of provider.
//
// References for the flat-rate shape: Stripe's legacy tax_rates object,
// Lago's integration-agnostic tax model, Chargebee's manual tax rules. All
// three concluded that the only safe manual behaviour is "apply what the
// tenant configured" — inferring cross-border zero-rating silently
// produces wrong invoices under OSS/OIDAR and in most US sales-tax regimes.
type ManualProvider struct {
	rateBP  int64
	taxName string
}

// NewManualProvider returns a provider that applies rateBP uniformly.
// taxName is the label shown on the invoice tax row ("VAT", "Sales Tax", …).
func NewManualProvider(rateBP int64, taxName string) *ManualProvider {
	return &ManualProvider{rateBP: rateBP, taxName: taxName}
}

func (*ManualProvider) Name() string { return "manual" }

func (m *ManualProvider) Calculate(_ context.Context, req Request) (*Result, error) {
	switch req.CustomerStatus {
	case StatusExempt:
		return exemptResult("manual", req, false, req.CustomerExemptReason), nil
	case StatusReverseCharge:
		return exemptResult("manual", req, true, ""), nil
	}

	lines := make([]ResultLine, len(req.LineItems))
	if m.rateBP <= 0 || len(req.LineItems) == 0 {
		for i, li := range req.LineItems {
			lines[i] = ResultLine{Ref: li.Ref, NetAmountCents: li.AmountCents}
		}
		return &Result{Provider: "manual", Lines: lines}, nil
	}

	subtotal := int64(0)
	for _, li := range req.LineItems {
		subtotal += li.AmountCents
	}

	if req.TaxInclusive {
		return m.calculateInclusive(req, lines, subtotal), nil
	}
	return m.calculateExclusive(req, lines, subtotal), nil
}

// calculateExclusive: line amounts are net; tax is added on top.
// Discount is distributed proportionally across lines before rate is applied.
func (m *ManualProvider) calculateExclusive(req Request, lines []ResultLine, subtotal int64) *Result {
	discount := min(max(req.DiscountCents, 0), subtotal)
	taxableBase := subtotal - discount
	if taxableBase <= 0 {
		for i, li := range req.LineItems {
			lines[i] = ResultLine{Ref: li.Ref, NetAmountCents: li.AmountCents}
		}
		return &Result{Provider: "manual", Lines: lines}
	}

	totalTax := money.RoundHalfToEven(taxableBase*m.rateBP, 10000)

	var (
		lineTaxSum      int64
		discountApplied int64
	)
	for i, li := range req.LineItems {
		var linePortion int64
		if i == len(req.LineItems)-1 {
			linePortion = discount - discountApplied
		} else {
			linePortion = money.RoundHalfToEven(discount*li.AmountCents, subtotal)
			discountApplied += linePortion
		}
		lineBase := max(li.AmountCents-linePortion, 0)
		lineTax := money.RoundHalfToEven(lineBase*m.rateBP, 10000)

		lines[i] = ResultLine{
			Ref:            li.Ref,
			NetAmountCents: li.AmountCents,
			TaxAmountCents: lineTax,
			TaxRateBP:      m.rateBP,
			TaxName:        m.taxName,
		}
		lineTaxSum += lineTax
	}

	if len(lines) > 0 && lineTaxSum != totalTax {
		lines[len(lines)-1].TaxAmountCents += totalTax - lineTaxSum
	}

	return m.wrap(totalTax, lines)
}

// calculateInclusive: line amounts are gross (tax-inclusive). Net and tax
// are carved out per line so engine-side subtotal == sum(Net) and
// subtotal + tax == sum(original gross).
//
// Discount is applied in GROSS terms (matches the semantics tenants expect
// from inclusive pricing: "10% off sticker"). Net and tax are derived from
// the post-discount gross base.
func (m *ManualProvider) calculateInclusive(req Request, lines []ResultLine, subtotal int64) *Result {
	discount := min(max(req.DiscountCents, 0), subtotal)
	taxableGross := subtotal - discount
	if taxableGross <= 0 {
		for i, li := range req.LineItems {
			lines[i] = ResultLine{Ref: li.Ref, NetAmountCents: li.AmountCents}
		}
		return &Result{Provider: "manual", Lines: lines}
	}

	denom := int64(10000 + m.rateBP)
	totalTax := taxableGross - money.RoundHalfToEven(taxableGross*10000, denom)

	var (
		lineTaxSum      int64
		discountApplied int64
	)
	for i, li := range req.LineItems {
		var linePortion int64
		if i == len(req.LineItems)-1 {
			linePortion = discount - discountApplied
		} else {
			linePortion = money.RoundHalfToEven(discount*li.AmountCents, subtotal)
			discountApplied += linePortion
		}
		lineGrossBase := max(li.AmountCents-linePortion, 0)
		lineNetBase := money.RoundHalfToEven(lineGrossBase*10000, denom)
		lineTax := lineGrossBase - lineNetBase
		// Full-line (undiscounted) net for NetAmountCents so the engine's
		// stored subtotal is undiscounted-net and DiscountCents is net-units,
		// matching what the invoice shows.
		lineNetUndisc := money.RoundHalfToEven(li.AmountCents*10000, denom)

		lines[i] = ResultLine{
			Ref:            li.Ref,
			NetAmountCents: lineNetUndisc,
			TaxAmountCents: lineTax,
			TaxRateBP:      m.rateBP,
			TaxName:        m.taxName,
		}
		lineTaxSum += lineTax
	}

	if len(lines) > 0 && lineTaxSum != totalTax {
		lines[len(lines)-1].TaxAmountCents += totalTax - lineTaxSum
	}

	return m.wrap(totalTax, lines)
}

func (m *ManualProvider) wrap(totalTax int64, lines []ResultLine) *Result {
	return &Result{
		Provider:        "manual",
		TotalTaxCents:   totalTax,
		EffectiveRateBP: m.rateBP,
		TaxName:         m.taxName,
		Lines:           lines,
		Breakdowns: []Breakdown{{
			Name:        m.taxName,
			RateBP:      m.rateBP,
			AmountCents: totalTax,
		}},
	}
}

func (*ManualProvider) Commit(_ context.Context, _, _ string) (string, error) { return "", nil }
