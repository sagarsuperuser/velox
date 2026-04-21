package payment

import (
	"context"
	"errors"
	"testing"

	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
	"github.com/sagarsuperuser/velox/internal/tenantstripe"
)

type stubCredentials struct {
	rows map[string]tenantstripe.PlaintextSecrets // key: tenantID + "|" + livemode
	err  map[string]error
}

func (s *stubCredentials) GetPlaintext(_ context.Context, tenantID string, livemode bool) (tenantstripe.PlaintextSecrets, error) {
	key := tenantID + "|"
	if livemode {
		key += "live"
	} else {
		key += "test"
	}
	if e, ok := s.err[key]; ok {
		return tenantstripe.PlaintextSecrets{}, e
	}
	if row, ok := s.rows[key]; ok {
		return row, nil
	}
	return tenantstripe.PlaintextSecrets{}, errs.ErrNotFound
}

func ctxForTenant(tenantID string, livemode bool) context.Context {
	ctx := context.WithValue(context.Background(), auth.TestTenantIDKey(), tenantID)
	return postgres.WithLivemode(ctx, livemode)
}

func TestStripeClients_ForCtx_ResolvesPerTenantMode(t *testing.T) {
	fetcher := &stubCredentials{rows: map[string]tenantstripe.PlaintextSecrets{
		"t1|live": {SecretKey: "sk_live_A"},
		"t1|test": {SecretKey: "sk_test_A"},
		"t2|live": {SecretKey: "sk_live_B"},
	}}
	c := NewStripeClients(fetcher)
	if c == nil {
		t.Fatal("NewStripeClients returned nil with fetcher set")
	}

	if sc := c.ForCtx(ctxForTenant("t1", true)); sc == nil {
		t.Error("ForCtx(t1 live) should return a client")
	}
	if sc := c.ForCtx(ctxForTenant("t1", false)); sc == nil {
		t.Error("ForCtx(t1 test) should return a client")
	}
	if sc := c.ForCtx(ctxForTenant("t2", false)); sc != nil {
		t.Error("ForCtx(t2 test) should be nil — not connected")
	}
}

func TestStripeClients_ForCtx_NilWithoutTenant(t *testing.T) {
	c := NewStripeClients(&stubCredentials{})
	if sc := c.ForCtx(context.Background()); sc != nil {
		t.Error("ForCtx without tenant in ctx must return nil")
	}
}

func TestStripeClients_NilBundle(t *testing.T) {
	c := NewStripeClients(nil)
	if c != nil {
		t.Error("NewStripeClients(nil) must return nil so boot code can gate on it")
	}
	if c.Has() {
		t.Error("(*StripeClients)(nil).Has() must be false")
	}
	if c.ForCtx(context.Background()) != nil {
		t.Error("(*StripeClients)(nil).ForCtx must be nil")
	}
	if c.For(context.Background(), "t1", true) != nil {
		t.Error("(*StripeClients)(nil).For must be nil")
	}
}

func TestStripeClients_For_ExplicitTenantMode(t *testing.T) {
	// Background paths (reconciler) have no auth ctx — they pass tenant +
	// mode explicitly from the invoice row. This must work regardless of
	// what's in ctx.
	fetcher := &stubCredentials{rows: map[string]tenantstripe.PlaintextSecrets{
		"t3|test": {SecretKey: "sk_test_X"},
	}}
	c := NewStripeClients(fetcher)
	if sc := c.For(context.Background(), "t3", false); sc == nil {
		t.Error("For(t3, test) should return a client")
	}
	if sc := c.For(context.Background(), "t3", true); sc != nil {
		t.Error("For(t3, live) should be nil — not connected")
	}
}

func TestStripeClients_For_LogsNonNotFoundErrors(t *testing.T) {
	// Simulate a DB error during credential lookup — resolver returns nil
	// (same as "not connected") but logs the underlying error. We can't
	// easily assert the log output here, so just confirm no panic and
	// nil return.
	fetcher := &stubCredentials{err: map[string]error{
		"t4|live": errors.New("boom"),
	}}
	c := NewStripeClients(fetcher)
	if sc := c.For(context.Background(), "t4", true); sc != nil {
		t.Error("For with DB error should return nil")
	}
}
