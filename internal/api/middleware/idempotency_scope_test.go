package middleware

import (
	"context"
	"testing"

	"github.com/sagarsuperuser/velox/internal/customerportal"
)

// TestScopeKeyToCustomer covers the pass-3 [security] audit finding: the
// idempotency row is keyed on (tenant, livemode, key) only, so two portal
// customers under the same tenant sending the same Idempotency-Key collided and
// could replay each other's cached responses. scopeKeyToCustomer namespaces the
// key with the acting customer so the rows are disjoint; operator requests
// (no portal customer) keep the raw key.
func TestScopeKeyToCustomer(t *testing.T) {
	t.Run("operator request: key unchanged", func(t *testing.T) {
		got := scopeKeyToCustomer(context.Background(), "idem-123")
		if got != "idem-123" {
			t.Errorf("got %q, want unchanged key for non-portal request", got)
		}
	})

	t.Run("portal customers get disjoint scopes", func(t *testing.T) {
		ctxA := customerportal.WithTestIdentity(context.Background(), "t1", "cus_A")
		ctxB := customerportal.WithTestIdentity(context.Background(), "t1", "cus_B")

		gotA := scopeKeyToCustomer(ctxA, "idem-123")
		gotB := scopeKeyToCustomer(ctxB, "idem-123")

		if gotA == gotB {
			t.Fatalf("same scoped key for different customers (%q) — cross-customer replay still possible", gotA)
		}
		if gotA == "idem-123" || gotB == "idem-123" {
			t.Errorf("portal key was not namespaced: A=%q B=%q", gotA, gotB)
		}
	})

	t.Run("same customer + key is stable", func(t *testing.T) {
		ctx1 := customerportal.WithTestIdentity(context.Background(), "t1", "cus_A")
		ctx2 := customerportal.WithTestIdentity(context.Background(), "t1", "cus_A")
		if scopeKeyToCustomer(ctx1, "k") != scopeKeyToCustomer(ctx2, "k") {
			t.Error("scoped key must be deterministic for the same customer+key")
		}
	})
}
