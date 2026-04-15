package tax

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/stripe/stripe-go/v82"
	"github.com/stripe/stripe-go/v82/tax/calculation"
)

// StripeCalculator calls the Stripe Tax Calculations API for jurisdiction-based
// tax. On any Stripe API error it falls back to the provided ManualCalculator
// so billing is never blocked by a third-party outage.
type StripeCalculator struct {
	apiKey   string
	fallback *ManualCalculator
}

// NewStripeCalculator creates a Stripe Tax calculator.
// fallback is used when the Stripe API returns an error (resilience).
func NewStripeCalculator(apiKey string, fallback *ManualCalculator) *StripeCalculator {
	return &StripeCalculator{apiKey: apiKey, fallback: fallback}
}

func (s *StripeCalculator) CalculateTax(ctx context.Context, currency string, addr CustomerAddress, lineItems []LineItemInput) (*TaxResult, error) {
	if len(lineItems) == 0 {
		return &TaxResult{}, nil
	}

	// Require at minimum a country for Stripe Tax to work
	if addr.Country == "" {
		slog.Warn("stripe tax: no customer country, falling back to manual")
		return s.fallback.CalculateTax(ctx, currency, addr, lineItems)
	}

	stripe.Key = s.apiKey

	// Build Stripe Tax Calculation request
	stripeLineItems := make([]*stripe.TaxCalculationLineItemParams, len(lineItems))
	for i, li := range lineItems {
		ref := fmt.Sprintf("line_%d", i)
		stripeLineItems[i] = &stripe.TaxCalculationLineItemParams{
			Amount:      stripe.Int64(li.AmountCents),
			Quantity:    stripe.Int64(max(li.Quantity, 1)),
			Reference:   stripe.String(ref),
			TaxBehavior: stripe.String("exclusive"),
		}
	}

	params := &stripe.TaxCalculationParams{
		Currency: stripe.String(strings.ToLower(currency)),
		CustomerDetails: &stripe.TaxCalculationCustomerDetailsParams{
			AddressSource: stripe.String("billing"),
			Address: &stripe.AddressParams{
				Country:    stripe.String(addr.Country),
				PostalCode: stripe.String(addr.PostalCode),
			},
		},
		LineItems: stripeLineItems,
	}
	// Only include optional address fields if present
	if addr.Line1 != "" {
		params.CustomerDetails.Address.Line1 = stripe.String(addr.Line1)
	}
	if addr.City != "" {
		params.CustomerDetails.Address.City = stripe.String(addr.City)
	}
	if addr.State != "" {
		params.CustomerDetails.Address.State = stripe.String(addr.State)
	}

	// Expand line_items so we get per-line tax in the response
	params.AddExpand("line_items")

	calc, err := calculation.New(params)
	if err != nil {
		slog.Warn("stripe tax API error, falling back to manual",
			"error", err,
		)
		return s.fallback.CalculateTax(ctx, currency, addr, lineItems)
	}

	return s.mapResult(calc, lineItems)
}

// mapResult converts a Stripe TaxCalculation response into our TaxResult.
func (s *StripeCalculator) mapResult(calc *stripe.TaxCalculation, inputItems []LineItemInput) (*TaxResult, error) {
	totalTax := calc.TaxAmountExclusive

	// Derive effective rate in basis points from the total
	subtotal := int64(0)
	for _, li := range inputItems {
		subtotal += li.AmountCents
	}
	effectiveRateBP := 0
	if subtotal > 0 {
		effectiveRateBP = int(totalTax * 10000 / subtotal)
	}

	// Extract tax name and country from the first tax breakdown
	taxName := ""
	taxCountry := ""
	if len(calc.TaxBreakdown) > 0 {
		tb := calc.TaxBreakdown[0]
		if tb.TaxRateDetails != nil {
			taxName = string(tb.TaxRateDetails.TaxType)
			taxCountry = tb.TaxRateDetails.Country
		}
	}

	// Map per-line-item taxes
	taxes := make([]LineItemTax, len(inputItems))
	for i := range inputItems {
		taxes[i] = LineItemTax{
			Index:          i,
			TaxAmountCents: 0,
			TaxRateBP:      effectiveRateBP,
			TaxName:        taxName,
		}
	}

	// Match Stripe line items back to our input via reference
	if calc.LineItems != nil {
		for _, sli := range calc.LineItems.Data {
			idx := parseLineRef(sli.Reference)
			if idx >= 0 && idx < len(taxes) {
				taxes[idx].TaxAmountCents = sli.AmountTax

				// Try to get per-line rate from breakdown
				if len(sli.TaxBreakdown) > 0 && sli.TaxBreakdown[0].TaxRateDetails != nil {
					pctStr := sli.TaxBreakdown[0].TaxRateDetails.PercentageDecimal
					if pctStr != "" {
						if pct, err := strconv.ParseFloat(pctStr, 64); err == nil {
							taxes[idx].TaxRateBP = int(pct * 100)
						}
					}
					if sli.TaxBreakdown[0].TaxRateDetails.DisplayName != "" {
						taxes[idx].TaxName = sli.TaxBreakdown[0].TaxRateDetails.DisplayName
					}
				}
			}
		}
	}

	return &TaxResult{
		TotalTaxAmountCents: totalTax,
		TaxRateBP:           effectiveRateBP,
		TaxName:             taxName,
		TaxCountry:          taxCountry,
		LineItemTaxes:       taxes,
	}, nil
}

// parseLineRef extracts the index from a reference like "line_2".
func parseLineRef(ref string) int {
	if !strings.HasPrefix(ref, "line_") {
		return -1
	}
	n, err := strconv.Atoi(ref[5:])
	if err != nil {
		return -1
	}
	return n
}

func max(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
