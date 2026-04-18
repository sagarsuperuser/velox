package tenant

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sagarsuperuser/velox/internal/testutil"
)

func TestBootstrap_Success(t *testing.T) {
	db := testutil.SetupTestDB(t)
	h := NewBootstrapHandler(db)

	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"tenant_name":"Test Corp"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status: got %d, want 201. body: %s", rec.Code, rec.Body.String())
	}

	var resp bootstrapResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)

	if resp.Tenant.Name != "Test Corp" {
		t.Errorf("tenant name: got %q", resp.Tenant.Name)
	}
	if !strings.HasPrefix(resp.SecretKey, "vlx_secret_") {
		t.Errorf("secret key should start with vlx_secret_")
	}
	if !strings.HasPrefix(resp.PublicKey, "vlx_pub_") {
		t.Errorf("public key should start with vlx_pub_")
	}
	if resp.Tenant.ID == "" {
		t.Error("tenant ID should be set")
	}
}

func TestBootstrap_RaceSafe(t *testing.T) {
	db := testutil.SetupTestDB(t)
	h := NewBootstrapHandler(db)

	// First bootstrap
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"tenant_name":"First"}`))
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("first bootstrap: got %d", rec.Code)
	}

	// Second bootstrap — should be rejected
	req = httptest.NewRequest("POST", "/", strings.NewReader(`{"tenant_name":"Second"}`))
	rec = httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Errorf("second bootstrap: got %d, want 409", rec.Code)
	}
}

func TestBootstrap_TokenRequired(t *testing.T) {
	db := testutil.SetupTestDB(t)
	h := &BootstrapHandler{db: db, token: "my-secret-token"}

	// Without token
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"tenant_name":"Test"}`))
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("no token: got %d, want 403", rec.Code)
	}

	// With wrong token
	req = httptest.NewRequest("POST", "/", strings.NewReader(`{"tenant_name":"Test","token":"wrong"}`))
	rec = httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("wrong token: got %d, want 403", rec.Code)
	}

	// With correct token
	req = httptest.NewRequest("POST", "/", strings.NewReader(`{"tenant_name":"Test","token":"my-secret-token"}`))
	rec = httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Errorf("correct token: got %d, want 201. body: %s", rec.Code, rec.Body.String())
	}
}

func TestBootstrap_DefaultTenantName(t *testing.T) {
	db := testutil.SetupTestDB(t)
	h := NewBootstrapHandler(db)

	req := httptest.NewRequest("POST", "/", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	var resp bootstrapResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Tenant.Name != "Default Tenant" {
		t.Errorf("default name: got %q, want 'Default Tenant'", resp.Tenant.Name)
	}
}
