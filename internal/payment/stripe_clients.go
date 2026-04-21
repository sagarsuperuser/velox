package payment

import (
	"context"
	"errors"
	"log/slog"

	"github.com/stripe/stripe-go/v82"

	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
	"github.com/sagarsuperuser/velox/internal/tenantstripe"
)

// StripeCredentials resolves decrypted Stripe secrets for a (tenant, mode).
// Implemented by *tenantstripe.Service — payment depends only on this
// narrow interface so the credential store can evolve independently.
type StripeCredentials interface {
	GetPlaintext(ctx context.Context, tenantID string, livemode bool) (tenantstripe.PlaintextSecrets, error)
}

// StripeClients builds per-tenant *stripe.Client instances on demand. Each
// tenant brings their own Stripe keys (stored encrypted via tenantstripe);
// this resolver looks them up and constructs a client with the right secret.
//
// The type name is retained from the env-var-backed ancestor so call sites
// don't churn — callers still treat a nil return from ForCtx/For as "Stripe
// not configured for this tenant+mode" and surface ErrStripeNotConfigured.
type StripeClients struct {
	fetcher StripeCredentials
}

// NewStripeClients wires the per-tenant resolver. fetcher=nil returns nil so
// boot code can gate Stripe-dependent components on a single `if clients
// != nil` check, matching the ergonomics of the previous env-var version.
func NewStripeClients(fetcher StripeCredentials) *StripeClients {
	if fetcher == nil {
		return nil
	}
	return &StripeClients{fetcher: fetcher}
}

// ForCtx derives tenantID from auth ctx and livemode from postgres ctx,
// then returns the stripe client for those credentials. Used by
// authenticated request handlers where both values already flow through ctx.
func (c *StripeClients) ForCtx(ctx context.Context) *stripe.Client {
	if c == nil {
		return nil
	}
	tenantID := auth.TenantID(ctx)
	if tenantID == "" {
		return nil
	}
	return c.For(ctx, tenantID, postgres.Livemode(ctx))
}

// For returns the stripe client for an explicit (tenant, mode). Used by
// token-authenticated paths (public payment links) and background workers
// (reconciler) that derive tenantID + livemode from a database row instead
// of request ctx.
func (c *StripeClients) For(ctx context.Context, tenantID string, livemode bool) *stripe.Client {
	if c == nil || tenantID == "" {
		return nil
	}
	creds, err := c.fetcher.GetPlaintext(ctx, tenantID, livemode)
	if err != nil {
		if !errors.Is(err, errs.ErrNotFound) {
			slog.ErrorContext(ctx, "stripe credential lookup failed",
				"tenant_id", tenantID, "livemode", livemode, "error", err)
		}
		return nil
	}
	if creds.SecretKey == "" {
		return nil
	}
	return stripe.NewClient(creds.SecretKey)
}

// Has returns true iff a credential fetcher is wired. Used at server
// startup to gate Stripe-dependent components. A true return does NOT mean
// any specific tenant has credentials connected — only that the system CAN
// resolve per-tenant keys when asked.
func (c *StripeClients) Has() bool {
	return c != nil && c.fetcher != nil
}
