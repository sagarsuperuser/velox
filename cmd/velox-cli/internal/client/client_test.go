package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestAuthHeaderSent asserts the Bearer header is stamped on every
// authenticated call. The CLI relies on this — tests that omit it pass
// silently against tenants that don't enforce auth, and break in prod.
func TestAuthHeaderSent(t *testing.T) {
	var gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok": true}`))
	}))
	defer ts.Close()

	c := New(ts.URL, "vlx_secret_test")
	var out map[string]any
	if err := c.Get(context.Background(), "/v1/ping", nil, &out); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if gotAuth != "Bearer vlx_secret_test" {
		t.Errorf("Authorization = %q, want Bearer vlx_secret_test", gotAuth)
	}
}

func TestPostBodyAndHeaders(t *testing.T) {
	var gotMethod, gotContentType string
	var gotBody string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotContentType = r.Header.Get("Content-Type")
		buf := make([]byte, 256)
		n, _ := r.Body.Read(buf)
		gotBody = string(buf[:n])
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id": "x"}`))
	}))
	defer ts.Close()

	c := New(ts.URL, "k")
	var resp struct {
		ID string `json:"id"`
	}
	body := map[string]string{"email": "ops@example.com"}
	if err := c.Post(context.Background(), "/v1/invoices/in_x/send", body, &resp); err != nil {
		t.Fatalf("Post: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", gotMethod)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	if !strings.Contains(gotBody, `"email":"ops@example.com"`) {
		t.Errorf("body = %q, want email key present", gotBody)
	}
	if resp.ID != "x" {
		t.Errorf("decoded ID = %q, want x", resp.ID)
	}
}

func TestAPIErrorOnNon2xx(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error": {"message": "missing field"}}`))
	}))
	defer ts.Close()

	c := New(ts.URL, "k")
	var out map[string]any
	err := c.Get(context.Background(), "/v1/whatever", nil, &out)
	if err == nil {
		t.Fatal("expected error on 400, got nil")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", apiErr.StatusCode)
	}
	if !strings.Contains(apiErr.Body, "missing field") {
		t.Errorf("body should be surfaced; got %q", apiErr.Body)
	}
}

// TestDecodeErrorSurfaces — when the server returns garbage, the CLI
// should fail loud (never return an empty/zero struct silently).
func TestDecodeErrorSurfaces(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`not-json`))
	}))
	defer ts.Close()

	c := New(ts.URL, "k")
	var out struct {
		Total int `json:"total"`
	}
	err := c.Get(context.Background(), "/v1/whatever", nil, &out)
	if err == nil {
		t.Fatal("expected decode error, got nil")
	}
	if !strings.Contains(err.Error(), "decode response") {
		t.Errorf("error should mention decode failure; got %v", err)
	}
}

func TestQueryStringEncoding(t *testing.T) {
	var gotRawQuery string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRawQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer ts.Close()

	c := New(ts.URL, "k")
	var out map[string]any
	q := url.Values{}
	q.Set("customer_id", "cus_with space")
	q.Set("limit", "10")
	if err := c.Get(context.Background(), "/v1/subscriptions", q, &out); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !strings.Contains(gotRawQuery, "customer_id=cus_with+space") &&
		!strings.Contains(gotRawQuery, "customer_id=cus_with%20space") {
		t.Errorf("query not URL-encoded: %s", gotRawQuery)
	}
	if !strings.Contains(gotRawQuery, "limit=10") {
		t.Errorf("limit missing from query: %s", gotRawQuery)
	}
}

func TestNewTrimsTrailingSlash(t *testing.T) {
	c := New("http://example.com/", "k")
	if c.BaseURL != "http://example.com" {
		t.Errorf("BaseURL = %q, want trailing slash trimmed", c.BaseURL)
	}
}

func TestNewDefaultBaseURL(t *testing.T) {
	c := New("", "k")
	if c.BaseURL != DefaultBaseURL {
		t.Errorf("BaseURL = %q, want %q", c.BaseURL, DefaultBaseURL)
	}
}
