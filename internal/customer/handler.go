package customer

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
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
	return r
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	var input CreateInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	customer, err := h.svc.Create(r.Context(), tenantID, input)
	if errors.Is(err, errs.ErrAlreadyExists) {
		writeError(w, http.StatusConflict, "already_exists", err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, customer)
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
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to list customers")
		slog.Error("list customers", "error", err)
		return
	}

	if customers == nil {
		customers = []domain.Customer{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"data":  customers,
		"total": total,
	})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	customer, err := h.svc.Get(r.Context(), tenantID, id)
	if errors.Is(err, errs.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "customer not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to get customer")
		slog.Error("get customer", "error", err)
		return
	}

	writeJSON(w, http.StatusOK, customer)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	var input UpdateInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	customer, err := h.svc.Update(r.Context(), tenantID, id, input)
	if errors.Is(err, errs.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "customer not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, customer)
}

func (h *Handler) upsertBillingProfile(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	customerID := chi.URLParam(r, "id")

	var bp domain.CustomerBillingProfile
	if err := json.NewDecoder(r.Body).Decode(&bp); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	bp.CustomerID = customerID

	profile, err := h.svc.UpsertBillingProfile(r.Context(), tenantID, bp)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, profile)
}

func (h *Handler) getBillingProfile(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	customerID := chi.URLParam(r, "id")

	profile, err := h.svc.GetBillingProfile(r.Context(), tenantID, customerID)
	if errors.Is(err, errs.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "billing profile not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to get billing profile")
		slog.Error("get billing profile", "error", err)
		return
	}

	writeJSON(w, http.StatusOK, profile)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"error": code, "message": message})
}
