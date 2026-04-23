package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMiddleware_NoAuth(t *testing.T) {
	svc := NewService(newMemStore())
	mw := Middleware(svc)

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not reach handler")
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401", rec.Code)
	}
}

func TestMiddleware_InvalidKey(t *testing.T) {
	svc := NewService(newMemStore())
	mw := Middleware(svc)

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not reach handler")
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer vlx_secret_0000000000000000000000000000000000000000000000000000000000000000000000000000")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401", rec.Code)
	}
}

func TestMiddleware_ValidSecretKey(t *testing.T) {
	svc := NewService(newMemStore())
	result, _ := svc.CreateKey(t.Context(), "tenant1", CreateKeyInput{Name: "Test", KeyType: KeyTypeSecret})

	mw := Middleware(svc)

	var gotTenantID string
	var gotKeyType KeyType
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTenantID = TenantID(r.Context())
		gotKeyType = GetKeyType(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+result.RawKey)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", rec.Code)
	}
	if gotTenantID != "tenant1" {
		t.Errorf("tenant_id: got %q", gotTenantID)
	}
	if gotKeyType != KeyTypeSecret {
		t.Errorf("key_type: got %q, want secret", gotKeyType)
	}
}

func TestMiddleware_XAPIKeyFallback(t *testing.T) {
	svc := NewService(newMemStore())
	result, _ := svc.CreateKey(t.Context(), "t1", CreateKeyInput{Name: "Test", KeyType: KeyTypePublishable})

	mw := Middleware(svc)
	var gotKeyType KeyType
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKeyType = GetKeyType(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-API-Key", result.RawKey)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", rec.Code)
	}
	if gotKeyType != KeyTypePublishable {
		t.Errorf("key_type: got %q, want publishable", gotKeyType)
	}
}

func TestRequire_SecretKeyHasFullAccess(t *testing.T) {
	svc := NewService(newMemStore())
	result, _ := svc.CreateKey(t.Context(), "t1", CreateKeyInput{Name: "Secret", KeyType: KeyTypeSecret})

	perms := []Permission{
		PermCustomerRead, PermCustomerWrite, PermPricingRead, PermPricingWrite,
		PermSubscriptionRead, PermSubscriptionWrite, PermUsageRead, PermUsageWrite,
		PermInvoiceRead, PermInvoiceWrite, PermDunningRead, PermDunningWrite,
		PermAPIKeyRead, PermAPIKeyWrite,
	}

	for _, perm := range perms {
		t.Run(string(perm), func(t *testing.T) {
			handler := Middleware(svc)(Require(perm)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})))

			req := httptest.NewRequest("GET", "/", nil)
			req.Header.Set("Authorization", "Bearer "+result.RawKey)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Errorf("secret key should have %s, got status %d", perm, rec.Code)
			}
		})
	}
}

func TestRequire_PublishableKeyRestricted(t *testing.T) {
	svc := NewService(newMemStore())
	result, _ := svc.CreateKey(t.Context(), "t1", CreateKeyInput{Name: "Pub", KeyType: KeyTypePublishable})

	// Should have — reads only; publishable keys ship in browsers.
	allowed := []Permission{PermCustomerRead, PermUsageRead, PermSubscriptionRead, PermInvoiceRead}
	for _, perm := range allowed {
		t.Run("allowed_"+string(perm), func(t *testing.T) {
			handler := Middleware(svc)(Require(perm)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})))

			req := httptest.NewRequest("GET", "/", nil)
			req.Header.Set("Authorization", "Bearer "+result.RawKey)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Errorf("pub key should have %s, got %d", perm, rec.Code)
			}
		})
	}

	// Should NOT have — every write path is closed on publishable.
	denied := []Permission{
		PermCustomerWrite, PermUsageWrite,
		PermPricingWrite, PermSubscriptionWrite, PermInvoiceWrite, PermDunningRead, PermDunningWrite, PermAPIKeyWrite,
	}
	for _, perm := range denied {
		t.Run("denied_"+string(perm), func(t *testing.T) {
			handler := Middleware(svc)(Require(perm)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Fatal("should not reach handler")
			})))

			req := httptest.NewRequest("GET", "/", nil)
			req.Header.Set("Authorization", "Bearer "+result.RawKey)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusForbidden {
				t.Errorf("pub key should NOT have %s, got %d (want 403)", perm, rec.Code)
			}
		})
	}
}

func TestLivemodeFromRawKey(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		live bool
		ok   bool
	}{
		{"secret_live", "vlx_secret_live_" + "ff", true, true},
		{"secret_test", "vlx_secret_test_" + "aa", false, true},
		{"pub_live", "vlx_pub_live_abc", true, true},
		{"pub_test", "vlx_pub_test_abc", false, true},
		{"platform_live", "vlx_plat_live_xyz", true, true},
		{"empty", "", false, false},
		{"no_mode_infix", "vlx_cps_customer_portal_token", false, false},
		{"random", "not-a-key", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			live, ok := LivemodeFromRawKey(c.raw)
			if live != c.live || ok != c.ok {
				t.Errorf("got (live=%v, ok=%v), want (live=%v, ok=%v)", live, ok, c.live, c.ok)
			}
		})
	}
}

func TestLivemodeFromRequest(t *testing.T) {
	// Bearer header with live key
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer vlx_secret_live_"+"abc")
	if live, ok := LivemodeFromRequest(r); !ok || !live {
		t.Errorf("Bearer live: got (live=%v, ok=%v), want (true, true)", live, ok)
	}

	// X-API-Key fallback with test key
	r = httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-API-Key", "vlx_secret_test_xyz")
	if live, ok := LivemodeFromRequest(r); !ok || live {
		t.Errorf("X-API-Key test: got (live=%v, ok=%v), want (false, true)", live, ok)
	}

	// No auth header → not ok
	r = httptest.NewRequest("GET", "/", nil)
	if _, ok := LivemodeFromRequest(r); ok {
		t.Error("no auth header should return ok=false")
	}

	// Unknown bearer token → ok=false (not rejected here, just not our concern)
	r = httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer some-opaque-session-token")
	if _, ok := LivemodeFromRequest(r); ok {
		t.Error("unparseable bearer should return ok=false")
	}
}

func TestRequire_PlatformKeyOnlyTenants(t *testing.T) {
	svc := NewService(newMemStore())
	result, _ := svc.CreateKey(t.Context(), "t1", CreateKeyInput{Name: "Platform", KeyType: KeyTypePlatform})

	// Should have tenant access
	handler := Middleware(svc)(Require(PermTenantWrite)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+result.RawKey)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("platform key should have tenant:write, got %d", rec.Code)
	}

	// Should NOT have customer access
	handler2 := Middleware(svc)(Require(PermCustomerRead)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not reach handler")
	})))

	req2 := httptest.NewRequest("GET", "/", nil)
	req2.Header.Set("Authorization", "Bearer "+result.RawKey)
	rec2 := httptest.NewRecorder()
	handler2.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusForbidden {
		t.Errorf("platform key should NOT have customer:read, got %d", rec2.Code)
	}
}
