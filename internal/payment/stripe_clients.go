package payment

import (
	"context"

	stripeclient "github.com/stripe/stripe-go/v82/client"

	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

// StripeClients holds one stripe-go *client.API per mode. Callers select
// per request via ForCtx(ctx), which reads the livemode flag set by auth
// middleware. This replaces the SDK's package-global stripe.Key — the
// global cannot represent two keys at once and would race under
// concurrent live/test traffic.
//
// A nil client for a given mode means the operator hasn't configured that
// mode (empty env var). Callers must check for nil before making calls;
// the typical guard is "if c := clients.ForCtx(ctx); c == nil { return
// ErrModeNotConfigured }".
type StripeClients struct {
	Live *stripeclient.API
	Test *stripeclient.API
}

// NewStripeClients builds a StripeClients from the two secret keys. Returns
// nil iff both keys are empty (so a caller can treat "Stripe not configured"
// as "StripeClients is nil").
func NewStripeClients(liveKey, testKey string) *StripeClients {
	if liveKey == "" && testKey == "" {
		return nil
	}
	c := &StripeClients{}
	if liveKey != "" {
		c.Live = &stripeclient.API{}
		c.Live.Init(liveKey, nil)
	}
	if testKey != "" {
		c.Test = &stripeclient.API{}
		c.Test.Init(testKey, nil)
	}
	return c
}

// ForCtx returns the API client for the ctx's livemode. Returns nil if
// the caller's mode has no key configured — callers must guard.
func (c *StripeClients) ForCtx(ctx context.Context) *stripeclient.API {
	if c == nil {
		return nil
	}
	if postgres.Livemode(ctx) {
		return c.Live
	}
	return c.Test
}

// Has returns true if at least one mode has a client configured.
func (c *StripeClients) Has() bool {
	return c != nil && (c.Live != nil || c.Test != nil)
}
