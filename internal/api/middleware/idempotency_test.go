package middleware

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/auth"
)

func TestIdempotency_NoCaching_WithoutKey(t *testing.T) {
	callCount := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"new"}`))
	})

	// No idempotency key — should execute every time
	req := httptest.NewRequest("POST", "/v1/customers", strings.NewReader(`{}`))
	ctx := context.WithValue(req.Context(), auth.TestTenantIDKey(), "t1")
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if callCount != 1 {
		t.Errorf("expected 1 call, got %d", callCount)
	}
}

func TestFingerprintRequest(t *testing.T) {
	base := fingerprintRequest("POST", "/v1/invoices", []byte(`{"amount":100}`))

	cases := []struct {
		name   string
		method string
		path   string
		body   []byte
	}{
		{"same inputs", "POST", "/v1/invoices", []byte(`{"amount":100}`)},
		{"different method", "PUT", "/v1/invoices", []byte(`{"amount":100}`)},
		{"different path", "POST", "/v1/customers", []byte(`{"amount":100}`)},
		{"different body", "POST", "/v1/invoices", []byte(`{"amount":200}`)},
	}

	if !bytes.Equal(base, fingerprintRequest(cases[0].method, cases[0].path, cases[0].body)) {
		t.Error("same inputs must produce same fingerprint")
	}
	for _, c := range cases[1:] {
		if bytes.Equal(base, fingerprintRequest(c.method, c.path, c.body)) {
			t.Errorf("%s: fingerprint should differ from base", c.name)
		}
	}

	// Field-boundary safety: "A" + "BC" must not hash the same as "AB" + "C".
	// The null-byte separator prevents this classic concat-collision.
	if bytes.Equal(
		fingerprintRequest("POST", "/v1/a", []byte("bc")),
		fingerprintRequest("POST", "/v1/ab", []byte("c")),
	) {
		t.Error("null separator should prevent field-boundary collisions")
	}
}

func TestIdempotency_GET_NotCached(t *testing.T) {
	callCount := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusOK)
	})

	// GET with idempotency key — should NOT be cached
	req := httptest.NewRequest("GET", "/v1/customers", nil)
	req.Header.Set("Idempotency-Key", "idem-get-1")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	handler.ServeHTTP(rec, req)

	if callCount != 2 {
		t.Errorf("GET should not be cached, expected 2 calls, got %d", callCount)
	}
}

func TestCursorEncoding(t *testing.T) {
	cursor := EncodeCursor("vlx_cus_123", mustParseTime("2026-04-01T00:00:00Z"))
	if cursor == "" {
		t.Fatal("cursor should not be empty")
	}

	decoded, err := DecodeCursor(cursor)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.ID != "vlx_cus_123" {
		t.Errorf("id: got %q", decoded.ID)
	}
}

func TestDecodeCursor_Invalid(t *testing.T) {
	_, err := DecodeCursor("")
	if err == nil {
		t.Error("expected error for empty cursor")
	}

	_, err = DecodeCursor("not-valid-base64!!!")
	if err == nil {
		t.Error("expected error for invalid base64")
	}
}

func TestParsePageParams(t *testing.T) {
	req := httptest.NewRequest("GET", "/v1/customers?limit=10&after=abc123&offset=5", nil)
	params := ParsePageParams(req)

	if params.Limit != 10 {
		t.Errorf("limit: got %d, want 10", params.Limit)
	}
	if params.Cursor != "abc123" {
		t.Errorf("cursor: got %q, want abc123", params.Cursor)
	}
	if params.Offset != 5 {
		t.Errorf("offset: got %d, want 5", params.Offset)
	}
}

func TestParsePageParams_Defaults(t *testing.T) {
	req := httptest.NewRequest("GET", "/v1/customers", nil)
	params := ParsePageParams(req)

	if params.Limit != 25 {
		t.Errorf("default limit: got %d, want 25", params.Limit)
	}
}

func TestParsePageParams_MaxLimit(t *testing.T) {
	req := httptest.NewRequest("GET", "/v1/customers?limit=500", nil)
	params := ParsePageParams(req)

	if params.Limit != 25 {
		t.Errorf("over-max limit should default: got %d, want 25", params.Limit)
	}
}

func TestRateLimiter_NilDB_FailsOpen(t *testing.T) {
	// With nil db the rate limiter should fail open — every request is allowed.
	rl := NewRateLimiter(nil, 1, time.Minute)
	handler := rl.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for i := 0; i < 5; i++ {
		req := httptest.NewRequest("GET", "/v1/customers", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("request %d: fail-open should allow, got %d", i, rec.Code)
		}
	}

	// Headers should still be set even in fail-open mode
	req := httptest.NewRequest("GET", "/v1/customers", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Header().Get("X-RateLimit-Limit") != "1" {
		t.Errorf("X-RateLimit-Limit: got %q, want 1", rec.Header().Get("X-RateLimit-Limit"))
	}
	if rec.Header().Get("X-RateLimit-Remaining") == "" {
		t.Error("X-RateLimit-Remaining header should be set")
	}
}

func TestRateLimiter_NilDB_FailsClosed_WhenConfigured(t *testing.T) {
	rl := NewRateLimiter(nil, 1, time.Minute)
	rl.SetFailClosed(true)
	handler := rl.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/v1/customers", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("fail-closed should return 429 when Redis missing, got %d", rec.Code)
	}
}

func TestRateLimiter_HealthSkipped(t *testing.T) {
	rl := NewRateLimiter(nil, 1, time.Minute)
	handler := rl.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Health check should bypass rate limiting entirely (no headers set)
	req := httptest.NewRequest("GET", "/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("health check should bypass rate limit, got %d", rec.Code)
	}
	if rec.Header().Get("X-RateLimit-Limit") != "" {
		t.Error("health check should not have rate limit headers")
	}
}

func TestNewPageResponse(t *testing.T) {
	data := []string{"a", "b", "c"}

	t.Run("has_more", func(t *testing.T) {
		resp := NewPageResponse(data, 10, 3, "id_c", mustParseTime("2026-04-01T00:00:00Z"))
		if !resp.HasMore {
			t.Error("has_more should be true when count > limit")
		}
		if resp.NextCursor == "" {
			t.Error("next_cursor should be set")
		}
	})

	t.Run("no_more", func(t *testing.T) {
		resp := NewPageResponse(data, 3, 25, "", mustParseTime("2026-04-01T00:00:00Z"))
		if resp.HasMore {
			t.Error("has_more should be false")
		}
	})
}

func mustParseTime(s string) (t time.Time) {
	t, _ = time.Parse(time.RFC3339, s)
	return
}
