package credit

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

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
	r.Post("/grant", h.grant)
	r.Post("/adjust", h.adjust)
	r.Get("/balance/{customer_id}", h.getBalance)
	r.Get("/ledger/{customer_id}", h.listEntries)
	return r
}

func (h *Handler) grant(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	var input GrantInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	entry, err := h.svc.Grant(r.Context(), tenantID, input)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, entry)
}

func (h *Handler) adjust(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	var input AdjustInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	entry, err := h.svc.Adjust(r.Context(), tenantID, input)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, entry)
}

func (h *Handler) getBalance(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	customerID := chi.URLParam(r, "customer_id")

	bal, err := h.svc.GetBalance(r.Context(), tenantID, customerID)
	if errors.Is(err, errs.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "customer not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to get balance")
		slog.Error("get credit balance", "error", err)
		return
	}

	writeJSON(w, http.StatusOK, bal)
}

func (h *Handler) listEntries(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	customerID := chi.URLParam(r, "customer_id")

	entries, err := h.svc.ListEntries(r.Context(), ListFilter{
		TenantID:   tenantID,
		CustomerID: customerID,
		EntryType:  r.URL.Query().Get("entry_type"),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to list entries")
		slog.Error("list credit entries", "error", err)
		return
	}
	if entries == nil {
		entries = []domain.CreditLedgerEntry{}
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": entries})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"error": code, "message": message})
}
