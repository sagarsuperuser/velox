package feature

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/auth"
)

// mountFeatureRoutes wires the handler the same way the production router does:
// readGuard = PermAPIKeyRead, globalGuard = PermTenantWrite (platform-only),
// tenantGuard = PermAPIKeyWrite. The auth.Require guards read the key type the
// auth middleware stamped into ctx, so each test sets a key type via withKeyType.
func mountFeatureRoutes(h *Handler) chi.Router {
	r := chi.NewRouter()
	r.Mount("/feature-flags", h.Routes(
		auth.Require(auth.PermAPIKeyRead),
		auth.Require(auth.PermTenantWrite),
		auth.Require(auth.PermAPIKeyWrite),
	))
	return r
}

// withAuth stamps tenant + key type into the request context the way the auth
// middleware would, so the per-route guards and the override handlers see a
// realistic principal.
func withAuth(r *http.Request, tenantID string, kt auth.KeyType) *http.Request {
	ctx := auth.WithTenantID(r.Context(), tenantID)
	ctx = auth.WithKeyType(ctx, kt)
	return r.WithContext(ctx)
}

func doRequest(t *testing.T, router chi.Router, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// TestSetOverride_DerivesTenantFromAuth_NotURL is the IDOR regression: the
// override is written for the AUTHENTICATED tenant, never a tenant named in
// the request. Before the fix the handler read tenant_id from the URL, so a
// secret key for tenant A could write tenant B's override. The route no longer
// carries a {tenant_id} param at all; the body has no tenant field either.
func TestSetOverride_DerivesTenantFromAuth_NotURL(t *testing.T) {
	store := newMemStore()
	store.seedFlag("billing.auto_charge", true)
	h := NewHandler(NewService(store))
	router := mountFeatureRoutes(h)

	// Authenticated as tenant_A with a secret-tier key (holds apikey:write).
	req := httptest.NewRequest(http.MethodPut,
		"/feature-flags/billing.auto_charge/override",
		bytes.NewBufferString(`{"enabled":false}`))
	req = withAuth(req, "tenant_A", auth.KeyTypeSecret)

	rec := doRequest(t, router, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body %q)", rec.Code, rec.Body.String())
	}

	// The override must be recorded against tenant_A — the principal — and
	// nothing must have been written for any other tenant.
	if _, ok := store.overrides["billing.auto_charge:tenant_A"]; !ok {
		t.Fatalf("expected override written for authenticated tenant_A, store=%v", store.overrides)
	}
	if len(store.overrides) != 1 {
		t.Fatalf("expected exactly one override (tenant_A), got %d: %v", len(store.overrides), store.overrides)
	}
}

// TestRemoveOverride_DerivesTenantFromAuth deletes only the caller's own
// override. A pre-seeded override for a different tenant must survive.
func TestRemoveOverride_DerivesTenantFromAuth(t *testing.T) {
	store := newMemStore()
	store.seedFlag("billing.auto_charge", true)
	_ = store.SetOverride(context.Background(), "tenant_A", "billing.auto_charge", false)
	_ = store.SetOverride(context.Background(), "tenant_B", "billing.auto_charge", false)
	h := NewHandler(NewService(store))
	router := mountFeatureRoutes(h)

	// Authenticated as tenant_A.
	req := httptest.NewRequest(http.MethodDelete,
		"/feature-flags/billing.auto_charge/override", nil)
	req = withAuth(req, "tenant_A", auth.KeyTypeSecret)

	rec := doRequest(t, router, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body %q)", rec.Code, rec.Body.String())
	}

	if _, ok := store.overrides["billing.auto_charge:tenant_A"]; ok {
		t.Fatalf("expected tenant_A override deleted")
	}
	if _, ok := store.overrides["billing.auto_charge:tenant_B"]; !ok {
		t.Fatalf("tenant_B override must NOT be touched by tenant_A's request")
	}
}

// TestSetGlobal_PlatformOnly gates the global on/off switch to platform keys.
// A secret-tier key (no tenant:write) must be rejected with 403, and the
// global flag must stay unchanged.
func TestSetGlobal_PlatformOnly(t *testing.T) {
	store := newMemStore()
	store.seedFlag("billing.auto_charge", true)
	h := NewHandler(NewService(store))
	router := mountFeatureRoutes(h)

	// Secret key (holds apikey:write but NOT tenant:write) attempts the
	// global flip — this is the tamper path that must be refused.
	req := httptest.NewRequest(http.MethodPut, "/feature-flags/billing.auto_charge",
		bytes.NewBufferString(`{"enabled":false}`))
	req = withAuth(req, "tenant_A", auth.KeyTypeSecret)

	rec := doRequest(t, router, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-platform global flip, got %d (body %q)", rec.Code, rec.Body.String())
	}
	if !store.flags["billing.auto_charge"].Enabled {
		t.Fatalf("global flag must be unchanged after rejected flip")
	}

	// A platform key IS allowed.
	req2 := httptest.NewRequest(http.MethodPut, "/feature-flags/billing.auto_charge",
		bytes.NewBufferString(`{"enabled":false}`))
	req2 = withAuth(req2, "", auth.KeyTypePlatform)
	rec2 := doRequest(t, router, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200 for platform global flip, got %d (body %q)", rec2.Code, rec2.Body.String())
	}
	if store.flags["billing.auto_charge"].Enabled {
		t.Fatalf("platform key should have flipped global flag to disabled")
	}
}

// TestSetOverride_RejectsReadOnlyKey confirms the override write still needs
// apikey:write — a publishable (read-only) key must be refused.
func TestSetOverride_RejectsReadOnlyKey(t *testing.T) {
	store := newMemStore()
	store.seedFlag("billing.auto_charge", true)
	h := NewHandler(NewService(store))
	router := mountFeatureRoutes(h)

	req := httptest.NewRequest(http.MethodPut,
		"/feature-flags/billing.auto_charge/override",
		bytes.NewBufferString(`{"enabled":false}`))
	req = withAuth(req, "tenant_A", auth.KeyTypePublishable)

	rec := doRequest(t, router, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for read-only key override write, got %d (body %q)", rec.Code, rec.Body.String())
	}
	if len(store.overrides) != 0 {
		t.Fatalf("no override should be written by a rejected request, got %v", store.overrides)
	}
}
