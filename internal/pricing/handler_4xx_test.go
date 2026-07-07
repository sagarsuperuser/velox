package pricing

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/auth"
)

// Handler-layer 4xx coverage (code-quality audit gap #3): the pricing
// handlers had zero direct HTTP tests, so request-decode / validation /
// not-found branches were exercised only indirectly by the happy-path
// e2e. These pin the observable error contract.

func newTestHandler() *Handler {
	return NewHandler(NewService(newMemStore()))
}

func doJSON(t *testing.T, h func(http.ResponseWriter, *http.Request), method, target, body string, urlParams map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	ctx := auth.WithTenantID(req.Context(), "tnt_test")
	if len(urlParams) > 0 {
		rctx := chi.NewRouteContext()
		for k, v := range urlParams {
			rctx.URLParams.Add(k, v)
		}
		ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	}
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h(rr, req)
	return rr
}

func TestPricingHandler_BadJSON_Returns400(t *testing.T) {
	h := newTestHandler()
	cases := []struct {
		name string
		fn   func(http.ResponseWriter, *http.Request)
	}{
		{"createPlan", h.createPlan},
		{"createMeter", h.createMeter},
		{"createRatingRule", h.createRatingRule},
		{"createOverride", h.createOverride},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rr := doJSON(t, c.fn, http.MethodPost, "/", "{not json", nil)
			if rr.Code != http.StatusBadRequest {
				t.Errorf("%s bad JSON: got %d, want 400; body=%s", c.name, rr.Code, rr.Body.String())
			}
		})
	}
}

func TestPricingHandler_Validation_Returns422(t *testing.T) {
	h := newTestHandler()
	cases := []struct {
		name, body string
		fn         func(http.ResponseWriter, *http.Request)
	}{
		{"plan missing code", `{"name":"P","currency":"USD","billing_interval":"monthly"}`, h.createPlan},
		{"plan missing name", `{"code":"p1","currency":"USD","billing_interval":"monthly"}`, h.createPlan},
		{"plan bad billing_interval", `{"code":"p1","name":"P","currency":"USD","billing_interval":"weekly"}`, h.createPlan},
		{"plan negative base fee", `{"code":"p1","name":"P","currency":"USD","billing_interval":"monthly","base_amount_cents":-5}`, h.createPlan},
		{"meter missing name", `{"aggregation":"sum"}`, h.createMeter},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rr := doJSON(t, c.fn, http.MethodPost, "/", c.body, nil)
			if rr.Code != http.StatusUnprocessableEntity {
				t.Errorf("%s: got %d, want 422; body=%s", c.name, rr.Code, rr.Body.String())
			}
		})
	}
}

func TestPricingHandler_NotFound_Returns404(t *testing.T) {
	h := newTestHandler()
	cases := []struct {
		name string
		fn   func(http.ResponseWriter, *http.Request)
	}{
		{"getPlan", h.getPlan},
		{"getMeter", h.getMeter},
		{"getRatingRule", h.getRatingRule},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rr := doJSON(t, c.fn, http.MethodGet, "/vlx_missing", "", map[string]string{"id": "vlx_missing"})
			if rr.Code != http.StatusNotFound {
				t.Errorf("%s unknown id: got %d, want 404; body=%s", c.name, rr.Code, rr.Body.String())
			}
		})
	}
}
