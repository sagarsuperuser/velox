package customer

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/audit"
	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
)

type Handler struct {
	svc         *Service
	gdpr        *GDPRHandler
	auditLogger *audit.Logger
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// SetGDPR attaches the GDPR handler so its routes are served under /customers.
func (h *Handler) SetGDPR(gh *GDPRHandler) {
	h.gdpr = gh
}

// SetAuditLogger wires the audit logger. Currently used by the
// cost-dashboard token rotate endpoint (record that a rotation happened
// without leaking the plaintext token into the audit trail). Nil-safe —
// callers that don't audit just skip the entry.
func (h *Handler) SetAuditLogger(l *audit.Logger) {
	h.auditLogger = l
}

func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/", h.create)
	r.Get("/", h.list)
	r.Get("/{id}", h.get)
	r.Patch("/{id}", h.update)
	r.Route("/{id}/billing-profile", func(r chi.Router) {
		r.Put("/", h.upsertBillingProfile)
		r.Get("/", h.getBillingProfile)
	})
	// GDPR endpoints (data export + right to erasure)
	if h.gdpr != nil {
		r.Get("/{id}/export", h.gdpr.exportData)
		r.Post("/{id}/delete-data", h.gdpr.deleteData)
	}
	return r
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	var input CreateInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}

	customer, err := h.svc.Create(r.Context(), tenantID, input)
	if err != nil {
		respond.FromError(w, r, err, "customer")
		return
	}

	respond.JSON(w, r, http.StatusCreated, customer)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	filter := ListFilter{
		TenantID:   tenantID,
		Status:     r.URL.Query().Get("status"),
		ExternalID: r.URL.Query().Get("external_id"),
		Limit:      limit,
		Offset:     offset,
	}

	customers, total, err := h.svc.List(r.Context(), filter)
	if err != nil {
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(), "list customers", "error", err)
		return
	}

	if customers == nil {
		customers = []domain.Customer{}
	}

	respond.List(w, r, customers, total)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	customer, err := h.svc.Get(r.Context(), tenantID, id)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "customer")
		return
	}
	if err != nil {
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(), "get customer", "error", err)
		return
	}

	respond.JSON(w, r, http.StatusOK, customer)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	var input UpdateInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}

	customer, err := h.svc.Update(r.Context(), tenantID, id, input)
	if err != nil {
		respond.FromError(w, r, err, "customer")
		return
	}

	respond.JSON(w, r, http.StatusOK, customer)
}

func (h *Handler) upsertBillingProfile(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	customerID := chi.URLParam(r, "id")

	var bp domain.CustomerBillingProfile
	if err := json.NewDecoder(r.Body).Decode(&bp); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}
	bp.CustomerID = customerID

	profile, err := h.svc.UpsertBillingProfile(r.Context(), tenantID, bp)
	if err != nil {
		respond.FromError(w, r, err, "billing profile")
		return
	}

	respond.JSON(w, r, http.StatusOK, profile)
}

func (h *Handler) getBillingProfile(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	customerID := chi.URLParam(r, "id")

	profile, err := h.svc.GetBillingProfile(r.Context(), tenantID, customerID)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "billing profile")
		return
	}
	if err != nil {
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(), "get billing profile", "error", err)
		return
	}

	respond.JSON(w, r, http.StatusOK, profile)
}
