package tax

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stripe/stripe-go/v82"

	"github.com/sagarsuperuser/velox/internal/platform/money"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

// stripeTaxFallbacks counts every time StripeCalculator falls through to its
// ManualCalculator fallback, labeled by reason. Operators use this to alert
// when Stripe Tax stops being usable (country unsupported, API outage, missing
// credentials) so invoices don't silently switch to the tenant's flat rate.
var stripeTaxFallbacks *prometheus.CounterVec

func init() {
	c := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "velox_tax_fallback_total",
		Help: "Count of Stripe tax calculator fallbacks to manual, by reason.",
	}, []string{"reason"})
	if err := prometheus.DefaultRegisterer.Register(c); err != nil {
		if are, ok := err.(prometheus.AlreadyRegisteredError); ok {
			stripeTaxFallbacks = are.ExistingCollector.(*prometheus.CounterVec)
		} else {
			panic(err)
		}
	} else {
		stripeTaxFallbacks = c
	}
}

// StripeCalculator calls the Stripe Tax Calculations API for jurisdiction-based
// tax. On any Stripe API error it falls back to the provided ManualCalculator
// so billing is never blocked by a third-party outage.
//
// Dual-key: holds per-mode clients and selects live/test based on ctx
// livemode. The tax calculation doesn't mutate Stripe state, so using the
// "wrong" account's API is merely incorrect accounting, not a destructive
// bug — but the mode split still matters because test keys are rate-limited
// differently and tests must not call the live endpoint.
type StripeCalculator struct {
	live     *stripe.Client
	test     *stripe.Client
	fallback *ManualCalculator
}

// NewStripeCalculator creates a Stripe Tax calculator. Each key may be empty;
// if only one is set, the other mode falls through to the manual calculator.
// fallback is used on any Stripe API error or when the mode's key is missing.
func NewStripeCalculator(liveKey, testKey string, fallback *ManualCalculator) *StripeCalculator {
	c := &StripeCalculator{fallback: fallback}
	if liveKey != "" {
		c.live = stripe.NewClient(liveKey)
	}
	if testKey != "" {
		c.test = stripe.NewClient(testKey)
	}
	return c
}

func (s *StripeCalculator) clientForCtx(ctx context.Context) *stripe.Client {
	if postgres.Livemode(ctx) {
		return s.live
	}
	return s.test
}

func (s *StripeCalculator) CalculateTax(ctx context.Context, currency string, addr CustomerAddress, lineItems []LineItemInput) (*TaxResult, error) {
	if len(lineItems) == 0 {
		return &TaxResult{}, nil
	}

	// Require at minimum a country for Stripe Tax to work
	if addr.Country == "" {
		slog.Warn("stripe tax: no customer country, falling back to manual")
		stripeTaxFallbacks.WithLabelValues("no_country").Inc()
		return s.fallback.CalculateTax(ctx, currency, addr, lineItems)
	}

	// Build Stripe Tax Calculation request
	stripeLineItems := make([]*stripe.TaxCalculationCreateLineItemParams, len(lineItems))
	for i, li := range lineItems {
		ref := fmt.Sprintf("line_%d", i)
		stripeLineItems[i] = &stripe.TaxCalculationCreateLineItemParams{
			Amount:      stripe.Int64(li.AmountCents),
			Quantity:    stripe.Int64(max(li.Quantity, 1)),
			Reference:   stripe.String(ref),
			TaxBehavior: stripe.String("exclusive"),
		}
	}

	params := &stripe.TaxCalculationCreateParams{
		Currency: stripe.String(strings.ToLower(currency)),
		CustomerDetails: &stripe.TaxCalculationCreateCustomerDetailsParams{
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

	sc := s.clientForCtx(ctx)
	if sc == nil {
		slog.Warn("stripe tax: no client configured for mode, falling back to manual",
			"livemode", postgres.Livemode(ctx),
		)
		stripeTaxFallbacks.WithLabelValues("no_client_for_mode").Inc()
		return s.fallback.CalculateTax(ctx, currency, addr, lineItems)
	}

	calc, err := sc.V1TaxCalculations.Create(ctx, params)
	if err != nil {
		slog.Warn("stripe tax API error, falling back to manual",
			"error", err,
		)
		stripeTaxFallbacks.WithLabelValues("api_error").Inc()
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
	// Round rather than truncate so the effective rate presented back to
	// callers is the nearest basis point. Truncation systematically biases
	// the displayed rate downward (e.g. 8.499% → 849 bp instead of 850).
	effectiveRateBP := int64(0)
	if subtotal > 0 {
		effectiveRateBP = money.RoundHalfToEven(totalTax*10000, subtotal)
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
							taxes[idx].TaxRateBP = int64(pct * 100)
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
