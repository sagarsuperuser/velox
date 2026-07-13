package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	chimw "github.com/go-chi/chi/v5/middleware"

	mw "github.com/sagarsuperuser/velox/internal/api/middleware"
)

// serve runs h behind mw.RequestID and returns the request id the handler
// observed through chimw.GetReqID — the SAME accessor audit.Logger.Log /
// LogInTx use to stamp audit_log.request_id, so what this test observes is
// exactly what lands on the row.
func serve(t *testing.T, headers map[string]string) string {
	t.Helper()

	var seen string
	h := mw.RequestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = chimw.GetReqID(r.Context())
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/customers", nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	h.ServeHTTP(httptest.NewRecorder(), req)
	return seen
}

// TestRequestID_IgnoresClientSuppliedHeader is the security assertion: the
// request id recorded against a request — and therefore onto that request's
// append-only audit_log rows — must be server-generated, never attacker-chosen.
//
// chi's stock middleware.RequestID uses an inbound X-Request-Id verbatim when
// present, which handed the client the forensic correlation id on its own audit
// rows. mw.RequestID replaces it. If someone ever swaps chi's back in, every
// subtest below fails.
func TestRequestID_IgnoresClientSuppliedHeader(t *testing.T) {
	const forged = "attacker-chosen-correlation-id"

	t.Run("inbound X-Request-Id is not honoured", func(t *testing.T) {
		got := serve(t, map[string]string{"X-Request-Id": forged})
		if got == forged {
			t.Fatalf("request id was taken from the client's X-Request-Id header: %q", got)
		}
		if strings.Contains(got, forged) {
			t.Fatalf("client-supplied value leaked into the request id: %q", got)
		}
		if !strings.HasPrefix(got, "req_") {
			t.Fatalf("request id %q is not server-minted (want the req_ prefix)", got)
		}
	})

	// chi reads the header by its canonical form, so a differently-cased or
	// alternately-named spelling must not sneak through either.
	t.Run("header-name variants are not honoured", func(t *testing.T) {
		for _, name := range []string{"x-request-id", "X-REQUEST-ID", "X-Request-ID", "Request-Id", "X-Correlation-Id"} {
			got := serve(t, map[string]string{name: forged})
			if got == forged || strings.Contains(got, forged) {
				t.Errorf("header %q was honoured: request id = %q", name, got)
			}
		}
	})

	t.Run("an id is always minted", func(t *testing.T) {
		if got := serve(t, nil); !strings.HasPrefix(got, "req_") || len(got) <= len("req_") {
			t.Fatalf("no server request id minted: %q", got)
		}
	})

	// A shared/constant id would let one caller's rows be confused with
	// another's — the correlation column must actually discriminate.
	t.Run("ids are unique per request", func(t *testing.T) {
		seen := make(map[string]bool, 100)
		for i := 0; i < 100; i++ {
			id := serve(t, map[string]string{"X-Request-Id": forged})
			if seen[id] {
				t.Fatalf("duplicate request id minted: %q", id)
			}
			seen[id] = true
		}
	})
}
