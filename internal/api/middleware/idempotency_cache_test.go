package middleware

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/testutil"
)

// TestIdempotency_Caches5xx is the regression test for COR-6: a transient 500
// retry must replay the cached 500 rather than re-invoke the handler. The
// previous impl cached only 2xx, which meant a retry could re-run side effects
// the first attempt had already committed but failed to confirm back to the
// client (classic "Stripe charged the card but the 200 timed out" scenario).
func TestIdempotency_Caches5xx(t *testing.T) {
	db := testutil.SetupTestDB(t)
	tenantID := testutil.CreateTestTenant(t, db, "Idempotency 5xx")

	var calls atomic.Int64
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, `{"error":"upstream timeout"}`, http.StatusInternalServerError)
	})
	handler := Idempotency(db)(inner)

	// First call: handler runs, returns 500.
	rec1 := invokeWithKey(t, handler, tenantID, "idem-5xx", `{"amount":100}`)
	if rec1.Code != http.StatusInternalServerError {
		t.Fatalf("first call: got %d, want 500", rec1.Code)
	}
	if calls.Load() != 1 {
		t.Fatalf("first call: handler should have run once, got %d", calls.Load())
	}
	if rec1.Header().Get("Idempotent-Replayed") != "" {
		t.Fatal("first call: Idempotent-Replayed must not be set")
	}

	// Second call same key+body: must replay cached 500 without running handler.
	rec2 := invokeWithKey(t, handler, tenantID, "idem-5xx", `{"amount":100}`)
	if rec2.Code != http.StatusInternalServerError {
		t.Fatalf("replay: got %d, want 500", rec2.Code)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("replay: handler must not re-run, got calls=%d", got)
	}
	if rec2.Header().Get("Idempotent-Replayed") != "true" {
		t.Error("replay: Idempotent-Replayed=true must be set")
	}
	if rec2.Body.String() != rec1.Body.String() {
		t.Errorf("replay body mismatch:\n got: %s\nwant: %s", rec2.Body.String(), rec1.Body.String())
	}
}

// TestIdempotency_Caches4xx_ExceptConflictAndUnprocessable pins the nuance
// that 4xx responses ARE cached (to prevent retry-and-succeed after a real
// validation/authorization failure), but 409 and 422 specifically are NOT
// cached because they signal "this isn't the real first response": 409 from
// concurrent contention (retry may succeed after the conflict clears), 422
// typically from input validation (client is expected to fix the body, and
// our fingerprint check will catch the body change as a key-reuse error).
func TestIdempotency_Caches4xx_ExceptConflictAndUnprocessable(t *testing.T) {
	db := testutil.SetupTestDB(t)
	tenantID := testutil.CreateTestTenant(t, db, "Idempotency 4xx")

	cases := []struct {
		name       string
		status     int
		key        string
		wantCached bool
	}{
		{"400 bad request cached", http.StatusBadRequest, "idem-400", true},
		{"401 unauthorized cached", http.StatusUnauthorized, "idem-401", true},
		{"404 not found cached", http.StatusNotFound, "idem-404", true},
		{"409 conflict NOT cached", http.StatusConflict, "idem-409", false},
		{"422 unprocessable NOT cached", http.StatusUnprocessableEntity, "idem-422", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int64
			inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				http.Error(w, `{"error":"x"}`, tc.status)
			})
			handler := Idempotency(db)(inner)

			_ = invokeWithKey(t, handler, tenantID, tc.key, `{}`)
			rec2 := invokeWithKey(t, handler, tenantID, tc.key, `{}`)

			switch {
			case tc.wantCached && calls.Load() != 1:
				t.Errorf("cached status %d: handler should have run once, got %d", tc.status, calls.Load())
			case tc.wantCached && rec2.Header().Get("Idempotent-Replayed") != "true":
				t.Errorf("cached status %d: replay header missing on second call", tc.status)
			case !tc.wantCached && calls.Load() != 2:
				t.Errorf("uncached status %d: handler should run twice, got %d", tc.status, calls.Load())
			case !tc.wantCached && rec2.Header().Get("Idempotent-Replayed") != "":
				t.Errorf("uncached status %d: replay header must not be set", tc.status)
			}
		})
	}
}

// TestIdempotency_FingerprintMismatchStill422 confirms COR-6's broader caching
// didn't compromise the fingerprint-mismatch contract: reusing a key with a
// different body must still return 422 idempotency_error (not a replay of the
// first response under the wrong parameters).
func TestIdempotency_FingerprintMismatchStill422(t *testing.T) {
	db := testutil.SetupTestDB(t)
	tenantID := testutil.CreateTestTenant(t, db, "Idempotency Fingerprint")

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"new"}`))
	})
	handler := Idempotency(db)(inner)

	// First body: succeeds, cached.
	rec1 := invokeWithKey(t, handler, tenantID, "idem-fp", `{"amount":100}`)
	if rec1.Code != http.StatusCreated {
		t.Fatalf("first call: got %d, want 201", rec1.Code)
	}

	// Second call: same key, DIFFERENT body — must be 422 idempotency_error,
	// not a replay of the 201.
	rec2 := invokeWithKey(t, handler, tenantID, "idem-fp", `{"amount":200}`)
	if rec2.Code != http.StatusUnprocessableEntity {
		t.Fatalf("mismatch: got %d, want 422", rec2.Code)
	}
	if !strings.Contains(rec2.Body.String(), "idempotency_error") {
		t.Errorf("mismatch: expected idempotency_error in body, got: %s", rec2.Body.String())
	}
}

func invokeWithKey(t *testing.T, h http.Handler, tenantID, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/v1/invoices", strings.NewReader(body))
	req.Header.Set("Idempotency-Key", key)
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), auth.TestTenantIDKey(), tenantID)
	req = req.WithContext(ctx)
	req.Body = io.NopCloser(strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}
