package api

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/invoice"
	"github.com/sagarsuperuser/velox/internal/subscription"
	"github.com/sagarsuperuser/velox/internal/usage"
)

// customerPortalHandler provides customer-scoped views that compose
// data from multiple domains. Lives in the API package because it
// crosses domain boundaries — domain packages stay independent.
type customerPortalHandler struct {
	subs     *subscription.PostgresStore
	invoices *invoice.PostgresStore
	usage    *usage.PostgresStore
}

func newCustomerPortalHandler(subs *subscription.PostgresStore, invoices *invoice.PostgresStore, usage *usage.PostgresStore) *customerPortalHandler {
	return &customerPortalHandler{subs: subs, invoices: invoices, usage: usage}
}

func (h *customerPortalHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/{customer_id}/subscriptions", h.listSubscriptions)
	r.Get("/{customer_id}/invoices", h.listInvoices)
	r.Get("/{customer_id}/overview", h.overview)
	return r
}

func (h *customerPortalHandler) listSubscriptions(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	customerID := chi.URLParam(r, "customer_id")

	subs, err := h.subs.List(r.Context(), subscription.ListFilter{
		TenantID:   tenantID,
		CustomerID: customerID,
	})
	if err != nil {
		respond.InternalError(w, r)
		return
	}
	if subs == nil {
		subs = []domain.Subscription{}
	}
	respond.JSON(w, r, http.StatusOK, map[string]any{"data": subs})
}

func (h *customerPortalHandler) listInvoices(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	customerID := chi.URLParam(r, "customer_id")

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 25
	}

	invoices, total, err := h.invoices.List(r.Context(), invoice.ListFilter{
		TenantID:   tenantID,
		CustomerID: customerID,
		Limit:      limit,
	})
	if err != nil {
		respond.InternalError(w, r)
		return
	}
	if invoices == nil {
		invoices = []domain.Invoice{}
	}
	respond.JSON(w, r, http.StatusOK, map[string]any{"data": invoices, "total": total})
}

// overview returns a consolidated view of a customer: active subscriptions,
// recent invoices, and current-period usage summary.
func (h *customerPortalHandler) overview(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	customerID := chi.URLParam(r, "customer_id")

	subs, _ := h.subs.List(r.Context(), subscription.ListFilter{
		TenantID:   tenantID,
		CustomerID: customerID,
		Status:     "active",
	})

	invoices, _, _ := h.invoices.List(r.Context(), invoice.ListFilter{
		TenantID:   tenantID,
		CustomerID: customerID,
		Limit:      5,
	})

	if subs == nil {
		subs = []domain.Subscription{}
	}
	if invoices == nil {
		invoices = []domain.Invoice{}
	}

	respond.JSON(w, r, http.StatusOK, map[string]any{
		"customer_id":          customerID,
		"active_subscriptions": subs,
		"recent_invoices":      invoices,
	})
}
