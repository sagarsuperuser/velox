package tax

import (
	"context"
	"fmt"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// Resolver maps a tenant's configured tax_provider to a concrete Provider
// instance. Wired into the billing engine so per-tenant selection happens at
// invoice-build time without hard-coding provider construction into the
// engine itself.
type Resolver struct {
	stripe StripeClientResolver
}

// NewResolver returns a resolver that serves NoneProvider and ManualProvider
// unconditionally, plus StripeTaxProvider when stripeClients != nil. A nil
// stripeClients resolver still accepts tax_provider='stripe_tax' — callers
// fall back to a ManualProvider so billing continues when Stripe isn't
// wired (e.g. local dev without credentials).
func NewResolver(stripeClients StripeClientResolver) *Resolver {
	return &Resolver{stripe: stripeClients}
}

// Resolve picks the right Provider for the tenant. Unknown or empty
// provider string falls through to NoneProvider so a mis-seeded tenant
// can't take billing offline. tax_provider=stripe_tax without a wired
// Stripe client now defers the invoice (per ADR-041) instead of silently
// substituting manual; operators wanting manual must explicitly select
// tax_provider=manual at the tenant level.
func (r *Resolver) Resolve(_ context.Context, ts domain.TenantSettings) (Provider, error) {
	switch ts.TaxProvider {
	case "stripe_tax":
		if r.stripe == nil {
			return nil, fmt.Errorf("tax_provider=stripe_tax but no Stripe client wired — set tax_provider=manual or configure Stripe")
		}
		return NewStripeTaxProvider(r.stripe), nil
	case "manual":
		return NewManualProvider(ts.TaxRateBP, ts.TaxName), nil
	case "none", "":
		return NewNoneProvider(), nil
	default:
		return NewNoneProvider(), nil
	}
}
