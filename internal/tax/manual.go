package tax

import "context"

// ManualCalculator applies a fixed tax rate (in basis points) to all line items.
// This preserves the original billing engine behavior where tenants configure
// a global tax rate in their settings.
type ManualCalculator struct {
	taxRateBP int    // e.g. 1850 = 18.50%
	taxName   string // e.g. "Sales Tax", "VAT"
}

// NewManualCalculator creates a calculator that applies the given basis-point
// rate uniformly across all line items.
func NewManualCalculator(taxRateBP int, taxName string) *ManualCalculator {
	return &ManualCalculator{taxRateBP: taxRateBP, taxName: taxName}
}

func (m *ManualCalculator) CalculateTax(_ context.Context, _ string, _ CustomerAddress, lineItems []LineItemInput) (*TaxResult, error) {
	if m.taxRateBP <= 0 || len(lineItems) == 0 {
		return &TaxResult{}, nil
	}

	// Calculate subtotal
	subtotal := int64(0)
	for _, li := range lineItems {
		subtotal += li.AmountCents
	}

	if subtotal <= 0 {
		return &TaxResult{}, nil
	}

	// Total tax: subtotal * bp / 10000, with remainder-based rounding
	totalTax := subtotal * int64(m.taxRateBP) / 10000
	if (subtotal*int64(m.taxRateBP))%10000 >= 5000 {
		totalTax++
	}

	// Per-line-item tax with remainder adjustment on the last item
	taxes := make([]LineItemTax, len(lineItems))
	var lineTaxSum int64

	for i, li := range lineItems {
		lineTax := li.AmountCents * int64(m.taxRateBP) / 10000
		if (li.AmountCents*int64(m.taxRateBP))%10000 >= 5000 {
			lineTax++
		}
		taxes[i] = LineItemTax{
			Index:          i,
			TaxAmountCents: lineTax,
			TaxRateBP:      m.taxRateBP,
			TaxName:        m.taxName,
		}
		lineTaxSum += lineTax
	}

	// Adjust last line item so per-line taxes sum exactly to the invoice total
	if len(taxes) > 0 && lineTaxSum != totalTax {
		taxes[len(taxes)-1].TaxAmountCents += totalTax - lineTaxSum
	}

	return &TaxResult{
		TotalTaxAmountCents: totalTax,
		TaxRateBP:           m.taxRateBP,
		TaxName:             m.taxName,
		LineItemTaxes:       taxes,
	}, nil
}
