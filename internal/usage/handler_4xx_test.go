package usage

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sagarsuperuser/velox/internal/auth"
)

// Handler-layer 4xx coverage (code-quality audit gap #3): the usage
// ingest handlers had zero direct HTTP tests. These pin the
// request-shape guards that fire BEFORE the service/resolvers — the
// branches most likely to regress silently since the happy-path e2e
// never exercises them.

func usageReq(t *testing.T, fn func(http.ResponseWriter, *http.Request), body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body))
	req = req.WithContext(auth.WithTenantID(req.Context(), "tnt_test"))
	rr := httptest.NewRecorder()
	fn(rr, req)
	return rr
}

func TestUsageHandler_Ingest_BadJSON_Returns400(t *testing.T) {
	h := NewHandler(nil, nil, nil) // guards return before svc/resolvers
	if rr := usageReq(t, h.ingest, "{not json"); rr.Code != http.StatusBadRequest {
		t.Errorf("ingest bad JSON: got %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

func TestUsageHandler_Batch_ShapeGuards(t *testing.T) {
	h := NewHandler(nil, nil, nil)
	cases := []struct {
		name, body string
		want       int
	}{
		{"not an array", `{"event":"x"}`, http.StatusBadRequest},
		{"malformed json", `[{bad`, http.StatusBadRequest},
		{"empty array", `[]`, http.StatusBadRequest},
		{"over 1000 events", "[" + strings.TrimSuffix(strings.Repeat(`{},`, 1001), ",") + "]", http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if rr := usageReq(t, h.batchIngest, c.body); rr.Code != c.want {
				t.Errorf("%s: got %d, want %d; body=%s", c.name, rr.Code, c.want, rr.Body.String())
			}
		})
	}
}
