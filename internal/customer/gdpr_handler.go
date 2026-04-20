package customer

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/errs"
)

// GDPRHandler exposes GDPR data export and right-to-deletion endpoints.
type GDPRHandler struct {
	svc *GDPRService
}

// NewGDPRHandler creates a handler for GDPR endpoints.
func NewGDPRHandler(svc *GDPRService) *GDPRHandler {
	return &GDPRHandler{svc: svc}
}

// Routes returns a chi.Router with GDPR routes that should be mounted
// under /customers/{id}.
func (h *GDPRHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/{id}/export", h.exportData)
	r.Post("/{id}/delete-data", h.deleteData)
	return r
}

// exportData handles GET /v1/customers/{id}/export
// Returns a full JSON export of all customer data (GDPR right to portability).
func (h *GDPRHandler) exportData(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	customerID := chi.URLParam(r, "id")

	export, err := h.svc.ExportCustomerData(r.Context(), tenantID, customerID)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "customer")
		return
	}
	if err != nil {
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(), "gdpr export customer data", "customer_id", customerID, "error", err)
		return
	}

	respond.JSON(w, r, http.StatusOK, export)
}

// deleteData handles POST /v1/customers/{id}/delete-data
// Anonymizes customer PII and archives the customer (GDPR right to erasure).
func (h *GDPRHandler) deleteData(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	customerID := chi.URLParam(r, "id")

	err := h.svc.DeleteCustomerData(r.Context(), tenantID, customerID)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "customer")
		return
	}
	if err != nil {
		// Check if it's a validation error (active subscriptions)
		if errMsg := err.Error(); errMsg == "customer has active subscriptions; cancel them before deletion" {
			respond.Validation(w, r, errMsg)
			return
		}
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(), "gdpr delete customer data", "customer_id", customerID, "error", err)
		return
	}

	respond.JSON(w, r, http.StatusOK, map[string]string{
		"status":      "deleted",
		"customer_id": customerID,
		"message":     "Customer data has been anonymized and the account archived. Financial records are preserved for legal compliance.",
	})
}
