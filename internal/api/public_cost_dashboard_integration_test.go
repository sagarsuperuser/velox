package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sagarsuperuser/velox/internal/platform/clock"
	"github.com/sagarsuperuser/velox/internal/testutil"
)

// TestPublicCostDashboard_RotateAndRead exercises the end-to-end
// happy-path: operator authenticates with an API key, rotates the
// cost-dashboard token for a customer, then a separate unauthenticated
// request hits /v1/public/cost-dashboard/{token} and gets back the
// sanitised projection. Mirrors the hosted-invoice precedent in
// hostedinvoice/handler_test.go but exercises the public-iframe
// surface end-to-end through the real HTTP router.
func TestPublicCostDashboard_RotateAndRead(t *testing.T) {
	db := testutil.SetupTestDB(t)
	clk := clock.Real()
	srv := NewServer(db, clk)

	tenantID := testutil.CreateTestTenant(t, db, "Cost Dashboard Tenant")
	apiKey := createTestAPIKey(t, db, tenantID)
	auth := "Bearer " + apiKey

	ts := httptest.NewServer(srv)
	defer ts.Close()

	// Create a customer to rotate the token for.
	resp := doPost(t, ts, "/v1/customers", auth, map[string]any{
		"external_id":  "cus_costdash_e2e",
		"display_name": "Acme Cost Dashboard",
		"email":        "billing@acme.example",
	})
	assertStatus(t, resp, http.StatusCreated)
	custBody := readJSON(t, resp)
	customerID := custBody["id"].(string)

	// Operator rotates the token. Response carries token + public_url.
	resp = doPost(t, ts, "/v1/customers/"+customerID+"/rotate-cost-dashboard-token", auth, nil)
	assertStatus(t, resp, http.StatusOK)
	rotateBody := readJSON(t, resp)
	token, _ := rotateBody["token"].(string)
	if !strings.HasPrefix(token, "vlx_pcd_") {
		t.Errorf("token prefix: got %q, want vlx_pcd_", token)
	}
	publicURL, _ := rotateBody["public_url"].(string)
	if !strings.HasSuffix(publicURL, "/"+token) {
		t.Errorf("public_url: got %q, want suffix /%s", publicURL, token)
	}
	if !strings.HasPrefix(publicURL, "/v1/public/cost-dashboard/") {
		t.Errorf("public_url: got %q, want prefix /v1/public/cost-dashboard/", publicURL)
	}

	// Public GET with the token (NO auth header). Should return 200
	// with the sanitised projection.
	publicResp := doGet(t, ts, publicURL, "")
	assertStatus(t, publicResp, http.StatusOK)
	publicBody := readJSON(t, publicResp)

	// Sanitisation guarantees: NO email, NO billing-profile, NO
	// metadata, NO internal status fields.
	if _, ok := publicBody["email"]; ok {
		t.Errorf("public response leaked email field: %v", publicBody["email"])
	}
	if _, ok := publicBody["billing_profile"]; ok {
		t.Errorf("public response leaked billing_profile")
	}
	if _, ok := publicBody["metadata"]; ok {
		t.Errorf("public response leaked metadata")
	}
	if _, ok := publicBody["display_name"]; ok {
		t.Errorf("public response leaked display_name")
	}
	if _, ok := publicBody["external_id"]; ok {
		t.Errorf("public response leaked external_id")
	}

	// Required fields present.
	if publicBody["customer_id"] != customerID {
		t.Errorf("customer_id: got %v, want %s", publicBody["customer_id"], customerID)
	}
	if publicBody["tenant_id"] != tenantID {
		t.Errorf("tenant_id: got %v, want %s", publicBody["tenant_id"], tenantID)
	}
	// billing_period block exists (period.source surfaces whether the
	// customer has a subscription or fell into the empty-state branch).
	if _, ok := publicBody["billing_period"]; !ok {
		t.Errorf("missing billing_period")
	}
	for _, key := range []string{"usage", "totals", "thresholds", "warnings", "subscriptions"} {
		if _, ok := publicBody[key]; !ok {
			t.Errorf("missing %s", key)
		}
	}
}

// TestPublicCostDashboard_UnknownTokenReturns404 covers the negative
// path: a well-formed but unknown token resolves to 404, never 500.
// Defensive: confirms the lookup path doesn't panic on an unmatched
// token and the handler maps ErrNotFound → 404 cleanly.
func TestPublicCostDashboard_UnknownTokenReturns404(t *testing.T) {
	db := testutil.SetupTestDB(t)
	clk := clock.Real()
	srv := NewServer(db, clk)

	ts := httptest.NewServer(srv)
	defer ts.Close()

	// 64 hex chars after the prefix — same shape a real token has, but
	// guaranteed to not be in the DB.
	bogus := "vlx_pcd_" + strings.Repeat("0", 64)
	resp := doGet(t, ts, "/v1/public/cost-dashboard/"+bogus, "")
	assertStatus(t, resp, http.StatusNotFound)
}

// TestPublicCostDashboard_RotateRequiresAuth asserts the operator
// rotate endpoint is gated by API-key auth — no API key in the
// Authorization header must surface as 401, not 200 (otherwise anyone
// could mint themselves an embed URL).
func TestPublicCostDashboard_RotateRequiresAuth(t *testing.T) {
	db := testutil.SetupTestDB(t)
	clk := clock.Real()
	srv := NewServer(db, clk)

	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp := doPost(t, ts, "/v1/customers/vlx_cus_anything/rotate-cost-dashboard-token", "", nil)
	assertStatus(t, resp, http.StatusUnauthorized)
}
