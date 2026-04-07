package webhook

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
	r.Post("/endpoints", h.createEndpoint)
	r.Get("/endpoints", h.listEndpoints)
	r.Delete("/endpoints/{id}", h.deleteEndpoint)
	r.Get("/events", h.listEvents)
	r.Get("/events/{id}/deliveries", h.listDeliveries)
	return r
}

func (h *Handler) createEndpoint(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	var input CreateEndpointInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	result, err := h.svc.CreateEndpoint(r.Context(), tenantID, input)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, result)
}

func (h *Handler) listEndpoints(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	endpoints, err := h.svc.ListEndpoints(r.Context(), tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to list endpoints")
		slog.Error("list webhook endpoints", "error", err)
		return
	}
	if endpoints == nil {
		endpoints = []domain.WebhookEndpoint{}
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": endpoints})
}

func (h *Handler) deleteEndpoint(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	err := h.svc.DeleteEndpoint(r.Context(), tenantID, id)
	if errors.Is(err, errs.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "endpoint not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to delete endpoint")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *Handler) listEvents(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	events, err := h.svc.ListEvents(r.Context(), tenantID, 50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to list events")
		slog.Error("list webhook events", "error", err)
		return
	}
	if events == nil {
		events = []domain.WebhookEvent{}
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": events})
}

func (h *Handler) listDeliveries(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	eventID := chi.URLParam(r, "id")

	deliveries, err := h.svc.ListDeliveries(r.Context(), tenantID, eventID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to list deliveries")
		slog.Error("list webhook deliveries", "error", err)
		return
	}
	if deliveries == nil {
		deliveries = []domain.WebhookDelivery{}
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": deliveries})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"error": code, "message": message})
}
