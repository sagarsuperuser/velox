package tax

import (
	"context"

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

// Resolve picks the right Provider for the tenant. Always safe: unknown or
// empty provider string falls through to NoneProvider so a mis-seeded
// tenant can't take billing offline.
func (r *Resolver) Resolve(_ context.Context, ts domain.TenantSettings) (Provider, error) {
	manual := NewManualProvider(ts.TaxRateBP, ts.TaxName)
	switch ts.TaxProvider {
	case "stripe_tax":
		if r.stripe == nil {
			return manual, nil
		}
		return NewStripeTaxProvider(r.stripe, manual), nil
	case "manual":
		return manual, nil
	case "none", "":
		return NewNoneProvider(), nil
	default:
		return NewNoneProvider(), nil
	}
}
