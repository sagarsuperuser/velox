package tax

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stripe/stripe-go/v82"

	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

// stripeTaxOutcomes counts non-happy-path outcomes from StripeTaxProvider
// by outcome class and reason. Operators alert on it to catch Stripe Tax
// becoming unusable — the engine defers the invoice to tax_status=pending
// and the TaxRetrier reconciler picks it up on the next tick.
//
// Labels:
//
//	outcome ∈ {deferred}            (post-ADR-041; legacy "fallback" cut 2026-05-30)
//	reason  ∈ {no_country, no_client_for_mode, api_error}
//
// Happy-path calculations (successful Stripe Tax calls, exempt, reverse-charge)
// are not counted here — this vector is intentionally a failure-mode signal.
var stripeTaxOutcomes *prometheus.CounterVec

func init() {
	c := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "velox_tax_outcome_total",
		Help: "Count of non-happy Stripe tax outcomes, by outcome (deferred) and reason.",
	}, []string{"outcome", "reason"})
	if err := prometheus.DefaultRegisterer.Register(c); err != nil {
		if are, ok := err.(prometheus.AlreadyRegisteredError); ok {
			stripeTaxOutcomes = are.ExistingCollector.(*prometheus.CounterVec)
		} else {
			panic(err)
		}
	} else {
		stripeTaxOutcomes = c
	}
}

// StripeClientResolver returns the *stripe.Client for the caller's context.
// Satisfied by *payment.StripeClients, which derives per-tenant credentials
// from ctx (tenant_id + livemode). Decoupled as an interface so the tax
// package does not depend on the payment package.
type StripeClientResolver interface {
	ForCtx(ctx context.Context) *stripe.Client
}

// StripeTaxProvider calls the Stripe Tax API. Calculate creates a
// tax_calculation; Commit creates a tax_transaction from the earlier
// calculation reference at invoice-finalize time. The transaction is what
// makes the tax decision durable upstream (calculations expire in 24 hours,
// transactions are permanent and show up in Stripe's tax reporting).
//
// On any Stripe API error Calculate returns the error so the engine can
// defer the invoice to tax_status=pending; the TaxRetrier reconciler picks
// it up on the next scheduler tick. The legacy "fallback to ManualProvider"
// branch was cut 2026-05-30 per ADR-041 (it silently substituted zero tax
// when no manual rate matched the jurisdiction, overriding operator intent).
//
// Multi-tenant: the Stripe client is resolved per ctx, so each tenant's
// calculation and commit hit their own Stripe account. A calculation itself
// doesn't mutate external state, but a tax_transaction does, so routing to
// the correct account matters.
type StripeTaxProvider struct {
	clients StripeClientResolver
}

// NewStripeTaxProvider wires a per-tenant Stripe client resolver. A nil
// resolver or a resolver that returns nil for a given ctx causes Calculate
// to defer the invoice — operator gets an actionable signal via the
// invoice's Attention banner instead of a silent zero-tax invoice.
func NewStripeTaxProvider(clients StripeClientResolver) *StripeTaxProvider {
	return &StripeTaxProvider{clients: clients}
}

func (*StripeTaxProvider) Name() string { return "stripe_tax" }

func (p *StripeTaxProvider) clientForCtx(ctx context.Context) *stripe.Client {
	if p.clients == nil {
		return nil
	}
	return p.clients.ForCtx(ctx)
}

func (p *StripeTaxProvider) Calculate(ctx context.Context, req Request) (*Result, error) {
	switch req.CustomerStatus {
	case StatusExempt:
		return exemptResult("stripe_tax", req, false, req.CustomerExemptReason), nil
	case StatusReverseCharge:
		return exemptResult("stripe_tax", req, true, ""), nil
	}
	if len(req.LineItems) == 0 {
		return &Result{Provider: "stripe_tax"}, nil
	}

	// Provider-connection check runs BEFORE customer-data validation.
	// Reason: when a tenant has tax_provider=stripe_tax but no Stripe
	// credentials connected for the active mode, the underlying issue
	// is configuration, not customer data — and the operator sees the
	// correct, actionable reason ("provider_not_configured") instead
	// of a misleading "no_country" or other downstream failure that
	// only fires because the client never got built. Also avoids any
	// pretence of touching Stripe — clientForCtx returns nil from a
	// pure local credential lookup when nothing is connected.
	client := p.clientForCtx(ctx)
	if client == nil {
		return p.handleFailure(ctx, req, "no_client_for_mode",
			fmt.Errorf("stripe tax: no Stripe credentials connected for livemode=%v — connect Stripe in Settings → Payments or change tax provider", postgres.Livemode(ctx)))
	}

	// Stripe Tax needs at minimum a country to resolve jurisdiction.
	if req.CustomerAddress.Country == "" {
		return p.handleFailure(ctx, req, "no_country",
			fmt.Errorf("stripe tax: customer has no country on billing profile"))
	}

	params := p.buildParams(req)

	// Serialize the outbound params before the call so we can persist a
	// faithful audit snapshot even on error. Stripe's SDK types marshal
	// cleanly enough for the tax_calculations.request JSONB column.
	reqRaw, _ := json.Marshal(params)

	calc, err := client.V1TaxCalculations.Create(ctx, params)
	if err != nil {
		return p.handleFailure(ctx, req, "api_error", fmt.Errorf("stripe tax: %w", err))
	}

	result, err := p.mapResult(calc, req)
	if err != nil {
		return nil, err
	}
	result.RequestRaw = reqRaw
	result.ResponseRaw, _ = json.Marshal(calc)
	return result, nil
}

// handleFailure defers the invoice for retry when Stripe Tax can't
// produce a real calculation. The engine routes the returned error
// into tax_status=pending so the TaxRetrier reconciler picks it up
// on the next scheduler tick. Per ADR-041 (2026-05-30) this is the
// only behavior — the legacy "fallback to ManualProvider" branch
// was cut because it silently produced zero tax when no manual rate
// matched the customer's jurisdiction, overriding the operator's
// intent (charge SOME tax) with a wrong answer (charge zero).
// Operators who genuinely want manual-rate billing set
// tax_provider=manual at the tenant level; mixed Stripe+manual is no
// longer expressible.
func (p *StripeTaxProvider) handleFailure(ctx context.Context, _ Request, reason string, failErr error) (*Result, error) {
	slog.Warn("stripe tax failed, deferring invoice for retry",
		"reason", reason, "error", failErr,
		"livemode", postgres.Livemode(ctx),
	)
	stripeTaxOutcomes.WithLabelValues("deferred", reason).Inc()
	return nil, failErr
}

// Commit creates a tax_transaction from an earlier calculation. Called at
// invoice finalize time. Idempotent via the invoice-scoped reference so a
// retried finalize does not create duplicate transactions.
func (p *StripeTaxProvider) Commit(ctx context.Context, calcRef, invoiceID string) (string, error) {
	if calcRef == "" {
		return "", nil
	}
	client := p.clientForCtx(ctx)
	if client == nil {
		// No client for mode — the calculation was a fallback result that
		// has no Stripe calc_id to commit. No-op, consistent with manual.
		return "", nil
	}
	params := &stripe.TaxTransactionCreateFromCalculationParams{
		Calculation: stripe.String(calcRef),
		Reference:   stripe.String(invoiceID),
	}
	txn, err := client.V1TaxTransactions.CreateFromCalculation(ctx, params)
	if err != nil {
		return "", fmt.Errorf("stripe tax: commit calculation %s for invoice %s: %w", calcRef, invoiceID, err)
	}
	return txn.ID, nil
}

// Reverse issues a reversal against a previously committed tax
// transaction. Called from the credit note issue path. The reference is
// derived from the credit note id so a retried issue does not create
// duplicate reversals — Stripe enforces reference uniqueness across all
// transactions in the account.
func (p *StripeTaxProvider) Reverse(ctx context.Context, req ReversalRequest) (*ReversalResult, error) {
	if req.OriginalTransactionID == "" {
		return nil, fmt.Errorf("stripe tax: reverse: original transaction id required")
	}
	ref := req.Reference
	if ref == "" {
		ref = req.CreditNoteID
	}
	if ref == "" {
		return nil, fmt.Errorf("stripe tax: reverse: reference required")
	}
	client := p.clientForCtx(ctx)
	if client == nil {
		return nil, fmt.Errorf("stripe tax: reverse: no client for context (livemode=%t)",
			postgres.Livemode(ctx))
	}
	mode := req.Mode
	if mode == "" {
		mode = ReversalModeFull
	}
	params := &stripe.TaxTransactionCreateReversalParams{
		OriginalTransaction: stripe.String(req.OriginalTransactionID),
		Reference:           stripe.String(ref),
		Mode:                stripe.String(string(mode)),
	}
	if mode == ReversalModePartial {
		if req.GrossAmountCents <= 0 {
			return nil, fmt.Errorf("stripe tax: reverse: partial mode requires positive gross amount")
		}
		// Stripe requires the amount in negative smallest-currency units.
		params.FlatAmount = stripe.Int64(-req.GrossAmountCents)
	}
	// Defense-in-depth idempotency: Reference uniqueness gives Stripe-
	// side semantic dedup, but a transient network failure between
	// Stripe accepting and the SDK returning could otherwise leave the
	// caller in retry-with-unknown-state. Idempotency-Key on the
	// request makes Stripe return the cached response on retry. Same
	// pattern as payment.StripeRefunder. Key shape derived from the
	// reference so retries of the same logical reversal converge.
	params.IdempotencyKey = stripe.String("velox_tax_rev_" + ref)
	txn, err := client.V1TaxTransactions.CreateReversal(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("stripe tax: reverse transaction %s for credit note %s: %w",
			req.OriginalTransactionID, req.CreditNoteID, err)
	}
	return &ReversalResult{TransactionID: txn.ID}, nil
}

func (p *StripeTaxProvider) buildParams(req Request) *stripe.TaxCalculationCreateParams {
	taxBehavior := "exclusive"
	if req.TaxInclusive {
		taxBehavior = "inclusive"
	}

	lineItems := make([]*stripe.TaxCalculationCreateLineItemParams, len(req.LineItems))
	for i, li := range req.LineItems {
		ref := li.Ref
		if ref == "" {
			ref = fmt.Sprintf("line_%d", i)
		}
		code := li.TaxCode
		if code == "" {
			code = req.DefaultTaxCode
		}
		params := &stripe.TaxCalculationCreateLineItemParams{
			Amount:      stripe.Int64(li.AmountCents),
			Quantity:    stripe.Int64(max(li.Quantity, 1)),
			Reference:   stripe.String(ref),
			TaxBehavior: stripe.String(taxBehavior),
		}
		if code != "" {
			params.TaxCode = stripe.String(code)
		}
		lineItems[i] = params
	}

	params := &stripe.TaxCalculationCreateParams{
		Currency: stripe.String(strings.ToLower(req.Currency)),
		CustomerDetails: &stripe.TaxCalculationCreateCustomerDetailsParams{
			AddressSource: stripe.String("billing"),
			Address: &stripe.AddressParams{
				Country:    stripe.String(req.CustomerAddress.Country),
				PostalCode: stripe.String(req.CustomerAddress.PostalCode),
			},
		},
		LineItems: lineItems,
	}
	if req.CustomerAddress.Line1 != "" {
		params.CustomerDetails.Address.Line1 = stripe.String(req.CustomerAddress.Line1)
	}
	if req.CustomerAddress.Line2 != "" {
		params.CustomerDetails.Address.Line2 = stripe.String(req.CustomerAddress.Line2)
	}
	if req.CustomerAddress.City != "" {
		params.CustomerDetails.Address.City = stripe.String(req.CustomerAddress.City)
	}
	if req.CustomerAddress.State != "" {
		params.CustomerDetails.Address.State = stripe.String(req.CustomerAddress.State)
	}

	// Tax IDs enable reverse-charge validation and B2B VAT exemption. When
	// present we attach them so Stripe can flip the calculation to
	// reverse-charge on its own, in addition to our explicit StatusReverseCharge
	// routing upstream.
	if req.CustomerTaxID != "" && req.CustomerTaxIDType != "" {
		params.CustomerDetails.TaxIDs = []*stripe.TaxCalculationCreateCustomerDetailsTaxIDParams{{
			Type:  stripe.String(req.CustomerTaxIDType),
			Value: stripe.String(req.CustomerTaxID),
		}}
	}

	params.AddExpand("line_items")
	return params
}

func (p *StripeTaxProvider) mapResult(calc *stripe.TaxCalculation, req Request) (*Result, error) {
	totalTax := calc.TaxAmountExclusive
	if req.TaxInclusive {
		totalTax = calc.TaxAmountInclusive
	}

	subtotal := int64(0)
	for _, li := range req.LineItems {
		subtotal += li.AmountCents
	}
	effectiveRate := float64(0)
	if subtotal > 0 {
		// Precise effective rate as percent (4-decimal precision).
		// ADR-042/043 stored as the only rate column.
		effectiveRate = float64(totalTax) * 100 / float64(subtotal)
	}

	taxName := ""
	taxCountry := ""
	reverseCharge := false
	var breakdowns []Breakdown
	for _, tb := range calc.TaxBreakdown {
		if tb == nil {
			continue
		}
		name := ""
		rate := float64(0)
		juris := ""
		if tb.TaxRateDetails != nil {
			name = string(tb.TaxRateDetails.TaxType)
			if tb.TaxRateDetails.Country != "" {
				juris = tb.TaxRateDetails.Country
				if tb.TaxRateDetails.State != "" {
					juris = tb.TaxRateDetails.Country + "-" + tb.TaxRateDetails.State
				}
				if taxCountry == "" {
					taxCountry = tb.TaxRateDetails.Country
				}
			}
			if pct := tb.TaxRateDetails.PercentageDecimal; pct != "" {
				rate = parseStripeRate(pct)
			}
		}
		if taxName == "" {
			taxName = name
		}
		if tb.TaxabilityReason == "reverse_charge" {
			reverseCharge = true
		}
		breakdowns = append(breakdowns, Breakdown{
			Jurisdiction: juris,
			Name:         name,
			Rate:         rate,
			AmountCents:  tb.Amount,
		})
	}

	// Map per-line results back to input Ref so the engine matches them to
	// its own line items independent of Stripe's ordering.
	lines := make([]ResultLine, len(req.LineItems))
	for i, li := range req.LineItems {
		lines[i] = ResultLine{
			Ref:            li.Ref,
			NetAmountCents: li.AmountCents, // default; overwritten for inclusive below
			TaxRate:        effectiveRate,
			TaxName:        taxName,
		}
	}
	indexByRef := make(map[string]int, len(req.LineItems))
	for i, li := range req.LineItems {
		indexByRef[li.Ref] = i
	}
	if calc.LineItems != nil {
		for _, sli := range calc.LineItems.Data {
			if sli == nil {
				continue
			}
			idx, ok := indexByRef[sli.Reference]
			if !ok {
				idx = parseLineRef(sli.Reference)
			}
			if idx < 0 || idx >= len(lines) {
				continue
			}
			lines[idx].TaxAmountCents = sli.AmountTax
			if req.TaxInclusive {
				// In inclusive mode Stripe returns the gross sent in as Amount
				// and the carved tax. amount_tax + net == amount; derive net.
				lines[idx].NetAmountCents = sli.Amount - sli.AmountTax
			}
			if len(sli.TaxBreakdown) > 0 {
				bd := sli.TaxBreakdown[0]
				if bd.TaxRateDetails != nil {
					if bd.TaxRateDetails.DisplayName != "" {
						lines[idx].TaxName = bd.TaxRateDetails.DisplayName
					} else if bd.TaxRateDetails.TaxType != "" {
						lines[idx].TaxName = string(bd.TaxRateDetails.TaxType)
					}
					if bd.TaxRateDetails.PercentageDecimal != "" {
						lines[idx].TaxRate = parseStripeRate(bd.TaxRateDetails.PercentageDecimal)
					}
				}
				if bd.Jurisdiction != nil && bd.Jurisdiction.Country != "" {
					if bd.Jurisdiction.State != "" {
						lines[idx].Jurisdiction = bd.Jurisdiction.Country + "-" + bd.Jurisdiction.State
					} else {
						lines[idx].Jurisdiction = bd.Jurisdiction.Country
					}
				}
				// Persist Stripe's structured taxability_reason so the PDF and
				// dashboard can distinguish two zero-tax outcomes that read
				// identically on the totals row but require different legends:
				// reverse_charge needs the EU Art. 196 disclosure, while
				// not_collecting (merchant has no registration in this
				// jurisdiction) needs no legend at all. Treated as an opaque
				// string — Stripe may add new reasons over time.
				lines[idx].TaxabilityReason = string(bd.TaxabilityReason)
			}
			if li := req.LineItems[idx]; li.TaxCode != "" {
				lines[idx].TaxCode = li.TaxCode
			} else if req.DefaultTaxCode != "" {
				lines[idx].TaxCode = req.DefaultTaxCode
			}
		}
	}

	return &Result{
		Provider:      "stripe_tax",
		CalculationID: calc.ID,
		TotalTaxCents: totalTax,
		EffectiveRate: effectiveRate,
		TaxName:       taxName,
		TaxCountry:    taxCountry,
		ReverseCharge: reverseCharge,
		Lines:         lines,
		Breakdowns:    breakdowns,
	}, nil
}

// parseStripeRate parses a Stripe Tax `percentage_decimal` string (e.g.
// "8.875") into the precise percent rate as float64. Stripe documents
// this field as a string precisely to avoid lossy float round-trip;
// Velox stores it verbatim into the tax_rate NUMERIC(7,4) column per
// ADR-042/043. Returns 0 on parse failure (caller logs at the call
// site).
func parseStripeRate(pct string) float64 {
	if pct == "" {
		return 0
	}
	v, err := strconv.ParseFloat(pct, 64)
	if err != nil {
		return 0
	}
	return v
}

// parseLineRef extracts the index from a reference like "line_2". Used when
// a Stripe response's line reference doesn't match any request Ref (defensive
// fallback; Stripe echoes references verbatim in practice).
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
