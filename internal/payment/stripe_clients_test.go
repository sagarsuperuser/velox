package payment

import (
	"context"
	"testing"

	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

func TestStripeClients_ForCtx_RoutesByLivemode(t *testing.T) {
	c := NewStripeClients("sk_live_x", "sk_test_x")
	if c == nil {
		t.Fatal("NewStripeClients returned nil with both keys set")
	}

	live := postgres.WithLivemode(context.Background(), true)
	test := postgres.WithLivemode(context.Background(), false)

	if got := c.ForCtx(live); got != c.Live {
		t.Errorf("ForCtx(livemode=true) did not return Live client")
	}
	if got := c.ForCtx(test); got != c.Test {
		t.Errorf("ForCtx(livemode=false) did not return Test client")
	}
}

func TestStripeClients_ForCtx_NilForMissingMode(t *testing.T) {
	liveOnly := NewStripeClients("sk_live_x", "")
	if liveOnly == nil || liveOnly.Live == nil {
		t.Fatal("live-only setup did not produce a live client")
	}
	if liveOnly.Test != nil {
		t.Errorf("test client should be nil when key unset")
	}
	testCtx := postgres.WithLivemode(context.Background(), false)
	if liveOnly.ForCtx(testCtx) != nil {
		t.Errorf("ForCtx in test mode should return nil when only live key is set")
	}
}

func TestStripeClients_NilBundle(t *testing.T) {
	// Both empty → nil. Callers treat nil as "stripe not configured" and
	// skip wiring entirely. Has() must handle nil without panicking.
	c := NewStripeClients("", "")
	if c != nil {
		t.Errorf("NewStripeClients(\"\",\"\") should return nil")
	}
	if c.Has() {
		t.Errorf("(*StripeClients)(nil).Has() must be false")
	}
	if c.ForCtx(context.Background()) != nil {
		t.Errorf("(*StripeClients)(nil).ForCtx must be nil")
	}
}

func TestStripeClients_DefaultLivemode(t *testing.T) {
	// ctx with no livemode key set → defaults to true (live). This mirrors
	// postgres.Livemode's default so background workers without explicit
	// mode wiring still talk to the live account.
	c := NewStripeClients("sk_live_x", "sk_test_x")
	if got := c.ForCtx(context.Background()); got != c.Live {
		t.Errorf("default ctx should route to live client")
	}
}
