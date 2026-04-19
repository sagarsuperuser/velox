package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/sagarsuperuser/velox/internal/auth"
)

type stubAuditWriter struct {
	err   error
	calls int
}

func (s *stubAuditWriter) Write(_ context.Context, _, _, _, _, _, _, _ string) error {
	s.calls++
	return s.err
}

type stubSettings struct {
	failClosed bool
	lookupErr  error
}

func (s *stubSettings) IsAuditFailClosed(_ context.Context, _ string) (bool, error) {
	if s.lookupErr != nil {
		return false, s.lookupErr
	}
	return s.failClosed, nil
}

func tenantCtx(t *testing.T, r *http.Request, tenantID string) *http.Request {
	t.Helper()
	ctx := context.WithValue(r.Context(), auth.TestTenantIDKey(), tenantID)
	return r.WithContext(ctx)
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Thing", "yes")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"vlx_cus_1","display_name":"Acme"}`))
	})
}

func TestAudit_Success_FlushesHandlerResponse(t *testing.T) {
	writer := &stubAuditWriter{}
	mw := auditLogWith(writer, &stubSettings{})
	h := mw(okHandler())

	req := httptest.NewRequest("POST", "/v1/customers", strings.NewReader(`{}`))
	req = tenantCtx(t, req, "t1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Errorf("status: got %d, want 201", rec.Code)
	}
	if rec.Header().Get("X-Thing") != "yes" {
		t.Errorf("handler header X-Thing not propagated")
	}
	if !strings.Contains(rec.Body.String(), "Acme") {
		t.Errorf("body not flushed: %q", rec.Body.String())
	}
	if writer.calls != 1 {
		t.Errorf("audit writer calls: got %d, want 1", writer.calls)
	}
}

func TestAudit_FailOpen_FlushesAndEmitsMetric(t *testing.T) {
	before := testutil.ToFloat64(auditWriteErrors.WithLabelValues("t-open"))

	writer := &stubAuditWriter{err: errors.New("db is down")}
	mw := auditLogWith(writer, &stubSettings{failClosed: false})
	h := mw(okHandler())

	req := httptest.NewRequest("POST", "/v1/customers", strings.NewReader(`{}`))
	req = tenantCtx(t, req, "t-open")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Errorf("fail-open must preserve handler status: got %d, want 201", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Acme") {
		t.Errorf("fail-open must flush body; got %q", rec.Body.String())
	}

	after := testutil.ToFloat64(auditWriteErrors.WithLabelValues("t-open"))
	if after-before != 1 {
		t.Errorf("audit_write_errors_total{tenant_id=\"t-open\"}: delta=%v, want 1", after-before)
	}
}

func TestAudit_FailClosed_Returns503AndEmitsMetric(t *testing.T) {
	before := testutil.ToFloat64(auditWriteErrors.WithLabelValues("t-closed"))

	writer := &stubAuditWriter{err: errors.New("db is down")}
	mw := auditLogWith(writer, &stubSettings{failClosed: true})
	h := mw(okHandler())

	req := httptest.NewRequest("POST", "/v1/customers", strings.NewReader(`{}`))
	req = tenantCtx(t, req, "t-closed")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("fail-closed status: got %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"audit_error"`) {
		t.Errorf("fail-closed body: got %q, want audit_error envelope", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "Acme") {
		t.Errorf("fail-closed must NOT flush handler body; got %q", rec.Body.String())
	}
	// Handler-set headers must not leak on fail-closed — 503 payload is ours.
	if rec.Header().Get("X-Thing") != "" {
		t.Errorf("fail-closed must not propagate handler headers; got X-Thing=%q", rec.Header().Get("X-Thing"))
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("fail-closed Content-Type: got %q, want application/json", ct)
	}

	after := testutil.ToFloat64(auditWriteErrors.WithLabelValues("t-closed"))
	if after-before != 1 {
		t.Errorf("audit_write_errors_total{tenant_id=\"t-closed\"}: delta=%v, want 1", after-before)
	}
}

// A broken settings lookup must fail-safe to closed — a malfunctioning
// settings query silently downgrading a SOC-2 tenant to fail-open would
// be the exact compliance hole this feature exists to close.
func TestAudit_SettingsLookupError_FailsSafeClosed(t *testing.T) {
	writer := &stubAuditWriter{err: errors.New("db is down")}
	mw := auditLogWith(writer, &stubSettings{lookupErr: errors.New("settings unreachable")})
	h := mw(okHandler())

	req := httptest.NewRequest("POST", "/v1/customers", strings.NewReader(`{}`))
	req = tenantCtx(t, req, "t-unknown")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("unknown policy must fail-safe to 503: got %d", rec.Code)
	}
}

// Non-2xx handler responses must skip audit entirely (no-op) and flush
// the handler's error response verbatim — we only record successful
// mutations in audit_log.
func TestAudit_Non2xx_SkipsAudit(t *testing.T) {
	writer := &stubAuditWriter{err: errors.New("should not be called")}
	mw := auditLogWith(writer, &stubSettings{failClosed: true})

	errHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad"}`))
	})

	req := httptest.NewRequest("POST", "/v1/customers", strings.NewReader(`{}`))
	req = tenantCtx(t, req, "t-4xx")
	rec := httptest.NewRecorder()
	mw(errHandler).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", rec.Code)
	}
	if writer.calls != 0 {
		t.Errorf("audit writer must not be called for 4xx; got %d calls", writer.calls)
	}
}

// Non-mutating methods must bypass the middleware entirely — no buffering,
// no settings lookup, no audit write.
func TestAudit_GET_Bypassed(t *testing.T) {
	writer := &stubAuditWriter{err: errors.New("should not be called")}
	settings := &stubSettings{lookupErr: errors.New("should not be called")}
	mw := auditLogWith(writer, settings)
	h := mw(okHandler())

	req := httptest.NewRequest("GET", "/v1/customers", nil)
	req = tenantCtx(t, req, "t1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Errorf("status: got %d, want 201", rec.Code)
	}
	if writer.calls != 0 {
		t.Errorf("GET must not trigger audit; got %d calls", writer.calls)
	}
}
