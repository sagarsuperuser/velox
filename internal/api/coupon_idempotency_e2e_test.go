package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/platform/clock"
	"github.com/sagarsuperuser/velox/internal/testutil"
)

// TestE2E_CouponIdempotency proves the Idempotency middleware is wired into
// the /v1/coupons route group end-to-end. This catches regressions where the
// middleware is present at the package level but accidentally scoped away
// from a particular write path during router refactors.
//
// Scenarios exercised:
//  1. Replay: same key + same body → second response is the cached 201 with
//     Idempotent-Replayed: true and byte-identical body.
//  2. Fingerprint conflict: same key + different body → 422 idempotency_error.
//     This is the Stripe-compatible guard against clients recycling a key
//     across distinct operations, which would otherwise mask a real write.
func TestE2E_CouponIdempotency(t *testing.T) {
	db := testutil.SetupTestDB(t)
	clk := clock.NewFake(time.Date(2026, 4, 23, 0, 0, 0, 0, time.UTC))
	srv := NewServer(db, clk)

	tenantID := testutil.CreateTestTenant(t, db, "Coupon Idempotency E2E")
	apiKey := createTestAPIKey(t, db, tenantID)

	ts := httptest.NewServer(srv)
	defer ts.Close()

	auth := "Bearer " + apiKey
	idemKey := "coupon-idem-" + time.Now().Format("150405.000000000")

	body := map[string]any{
		"code":           "IDEMPOTENT10",
		"name":           "Idempotent 10%",
		"type":           "percentage",
		"percent_off_bp": 1000,
		"duration":       "once",
	}

	// First call — should hit the handler, create the coupon, cache the 201.
	first := doPostIdem(t, ts, "/v1/coupons", auth, idemKey, body)
	assertStatus(t, first, http.StatusCreated)
	if first.Header.Get("Idempotent-Replayed") == "true" {
		t.Error("first call must not be a replay")
	}
	firstBody := readJSON(t, first)
	firstID, _ := firstBody["id"].(string)
	if firstID == "" {
		t.Fatalf("first response missing id: %v", firstBody)
	}

	// Replay — identical key + body. Response must be byte-for-byte identical
	// (same id, same code) AND carry Idempotent-Replayed: true.
	second := doPostIdem(t, ts, "/v1/coupons", auth, idemKey, body)
	assertStatus(t, second, http.StatusCreated)
	if second.Header.Get("Idempotent-Replayed") != "true" {
		t.Error("second call must set Idempotent-Replayed: true")
	}
	secondBody := readJSON(t, second)
	if secondBody["id"] != firstID {
		t.Errorf("replay id: got %v, want %s", secondBody["id"], firstID)
	}
	if secondBody["code"] != "IDEMPOTENT10" {
		t.Errorf("replay code: got %v", secondBody["code"])
	}

	// Fingerprint conflict — same key, different body → 422. This is what
	// rescues us from a client that recycles a key across a percent change
	// or a code typo fix; without it the original write would silently win.
	conflictBody := map[string]any{
		"code":           "IDEMPOTENT10",
		"name":           "Idempotent 10% (bumped)",
		"type":           "percentage",
		"percent_off_bp": 2000, // different value → different fingerprint
		"duration":       "once",
	}
	third := doPostIdem(t, ts, "/v1/coupons", auth, idemKey, conflictBody)
	assertStatus(t, third, http.StatusUnprocessableEntity)
	errBody := readJSON(t, third)
	errObj, _ := errBody["error"].(map[string]any)
	if errObj == nil || errObj["type"] != "idempotency_error" {
		t.Errorf("fingerprint conflict: expected error.type=idempotency_error, got %v", errBody)
	}
}

// doPostIdem is doPost with an Idempotency-Key header. Kept as a small
// helper so the test body stays focused on what each call asserts rather
// than request-wiring boilerplate.
func doPostIdem(t *testing.T, ts *httptest.Server, path, auth, idemKey string, body map[string]any) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest("POST", ts.URL+path, &buf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", idemKey)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}
