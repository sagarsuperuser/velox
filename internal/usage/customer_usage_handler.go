package usage

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/errs"
)

// CustomerUsageHandler exposes GET /v1/customers/{id}/usage. Kept distinct
// from the existing usage.Handler (which owns /v1/usage-events ingest+list)
// because this surface composes across customer/subscription/pricing —
// the dependency set is materially different.
type CustomerUsageHandler struct {
	svc *CustomerUsageService
}

// NewCustomerUsageHandler wires a handler around a CustomerUsageService.
func NewCustomerUsageHandler(svc *CustomerUsageService) *CustomerUsageHandler {
	return &CustomerUsageHandler{svc: svc}
}

// CustomerUsageRoutes returns the sub-router. Mount at
// /v1/customers/{id}/usage with the requireRead guard (auth.PermUsageRead);
// the customer ID is read from chi.URLParam(r, "id") after the
// sibling-mount, mirroring the /customers/{id}/coupon precedent.
func (h *CustomerUsageHandler) CustomerUsageRoutes(requireRead func(http.Handler) http.Handler) chi.Router {
	r := chi.NewRouter()
	r.With(requireRead).Get("/", h.get)
	return r
}

func (h *CustomerUsageHandler) get(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	customerID := chi.URLParam(r, "id")
	if customerID == "" {
		respond.BadRequest(w, r, "customer id is required")
		return
	}

	period, err := parseUsagePeriodQuery(r)
	if err != nil {
		respond.FromError(w, r, err, "customer_usage")
		return
	}

	result, err := h.svc.Get(r.Context(), tenantID, customerID, period)
	if err != nil {
		respond.FromError(w, r, err, "customer")
		return
	}

	respond.JSON(w, r, http.StatusOK, result)
}

// parseUsagePeriodQuery reads ?from= and ?to= from the request URL. Empty
// query → zero-valued CustomerUsagePeriod (service defaults to current
// cycle). Unparseable values surface as 400 via the handler's FromError
// path, with the field set so the dashboard can route the message.
func parseUsagePeriodQuery(r *http.Request) (CustomerUsagePeriod, error) {
	var period CustomerUsagePeriod
	if v := r.URL.Query().Get("from"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return CustomerUsagePeriod{}, errs.Invalid("from", "must be RFC 3339 (e.g. 2026-04-01T00:00:00Z)")
		}
		period.From = t
	}
	if v := r.URL.Query().Get("to"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return CustomerUsagePeriod{}, errs.Invalid("to", "must be RFC 3339 (e.g. 2026-05-01T00:00:00Z)")
		}
		period.To = t
	}
	return period, nil
}
