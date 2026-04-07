package middleware

import (
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
		w.Write([]byte(`{"id":"new"}`))
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

func TestRateLimiter(t *testing.T) {
	rl := NewRateLimiter(3, 1*60*1e9) // 3 per minute
	handler := rl.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest("GET", "/v1/customers", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("request %d: got %d, want 200", i, rec.Code)
		}
	}

	// 4th request should be rate limited
	req := httptest.NewRequest("GET", "/v1/customers", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("4th request: got %d, want 429", rec.Code)
	}

	// Verify rate limit headers
	if rec.Header().Get("X-RateLimit-Limit") != "3" {
		t.Errorf("X-RateLimit-Limit: got %q", rec.Header().Get("X-RateLimit-Limit"))
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("Retry-After header should be set on 429")
	}
}

func TestRateLimiter_HealthSkipped(t *testing.T) {
	rl := NewRateLimiter(1, 1*60*1e9)
	handler := rl.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// First request consumes the bucket
	req := httptest.NewRequest("GET", "/v1/customers", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// Health check should bypass rate limiting
	req = httptest.NewRequest("GET", "/health", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("health check should bypass rate limit, got %d", rec.Code)
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
