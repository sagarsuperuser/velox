package auth

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

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
	r.Delete("/{id}", h.revoke)
	return r
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	tenantID := TenantID(r.Context())
	if tenantID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "not authenticated")
		return
	}

	var input CreateKeyInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	result, err := h.svc.CreateKey(r.Context(), tenantID, input)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, result)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	tenantID := TenantID(r.Context())
	if tenantID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "not authenticated")
		return
	}

	keys, err := h.svc.ListKeys(r.Context(), ListFilter{TenantID: tenantID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to list keys")
		slog.Error("list api keys", "error", err)
		return
	}
	if keys == nil {
		keys = []domain.APIKey{}
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": keys})
}

func (h *Handler) revoke(w http.ResponseWriter, r *http.Request) {
	tenantID := TenantID(r.Context())
	id := chi.URLParam(r, "id")

	key, err := h.svc.RevokeKey(r.Context(), tenantID, id)
	if errors.Is(err, errs.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "api key not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to revoke key")
		slog.Error("revoke api key", "error", err)
		return
	}

	writeJSON(w, http.StatusOK, key)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"error": code, "message": message})
}
