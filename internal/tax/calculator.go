package tax

import "context"

// TaxResult holds the output of a tax calculation for an entire invoice.
type TaxResult struct {
	TotalTaxAmountCents int64         // Sum of all line-item taxes
	TaxRateBP           int           // Effective rate in basis points (1850 = 18.50%)
	TaxName             string        // e.g. "CA Sales Tax", "VAT"
	TaxCountry          string        // ISO 3166-1 alpha-2
	LineItemTaxes       []LineItemTax // Per-line-item breakdown
}

// LineItemTax holds the tax for a single invoice line item.
type LineItemTax struct {
	Index          int // Position in the input slice
	TaxAmountCents int64
	TaxRateBP      int
	TaxName        string
}

// LineItemInput describes a line item for tax calculation purposes.
type LineItemInput struct {
	AmountCents int64
	Description string
	Quantity    int64
}

// CustomerAddress holds the address fields needed for jurisdiction-based tax.
type CustomerAddress struct {
	Line1      string
	City       string
	State      string
	PostalCode string
	Country    string // ISO 3166-1 alpha-2
}

// Calculator computes tax for a set of line items.
// Implementations include ManualCalculator (flat rate) and StripeCalculator (API-based).
type Calculator interface {
	CalculateTax(ctx context.Context, currency string, customerAddress CustomerAddress, lineItems []LineItemInput) (*TaxResult, error)
}
