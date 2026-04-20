package webhook

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/api/respond"
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
	r.Get("/endpoints/stats", h.getEndpointStats)
	r.Delete("/endpoints/{id}", h.deleteEndpoint)
	r.Post("/endpoints/{id}/rotate-secret", h.rotateSecret)
	r.Get("/events", h.listEvents)
	r.Get("/events/{id}/deliveries", h.listDeliveries)
	r.Post("/events/{id}/replay", h.replayEvent)
	return r
}

func (h *Handler) getEndpointStats(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	stats, err := h.svc.GetEndpointStats(r.Context(), tenantID)
	if err != nil {
		respond.InternalError(w, r)
		slog.Error("get webhook endpoint stats", "error", err)
		return
	}
	if stats == nil {
		stats = []EndpointStats{}
	}

	respond.JSON(w, r, http.StatusOK, map[string]any{"data": stats})
}

func (h *Handler) createEndpoint(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	var input CreateEndpointInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}

	result, err := h.svc.CreateEndpoint(r.Context(), tenantID, input)
	if err != nil {
		respond.FromError(w, r, err, "webhook_endpoint")
		return
	}

	respond.JSON(w, r, http.StatusCreated, result)
}

func (h *Handler) listEndpoints(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	endpoints, err := h.svc.ListEndpoints(r.Context(), tenantID)
	if err != nil {
		respond.InternalError(w, r)
		slog.Error("list webhook endpoints", "error", err)
		return
	}
	if endpoints == nil {
		endpoints = []domain.WebhookEndpoint{}
	}

	respond.JSON(w, r, http.StatusOK, map[string]any{"data": endpoints})
}

func (h *Handler) deleteEndpoint(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	err := h.svc.DeleteEndpoint(r.Context(), tenantID, id)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "endpoint")
		return
	}
	if err != nil {
		respond.InternalError(w, r)
		return
	}

	respond.JSON(w, r, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *Handler) rotateSecret(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	newSecret, err := h.svc.RotateSecret(r.Context(), tenantID, id)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "webhook endpoint")
		return
	}
	if err != nil {
		respond.InternalError(w, r)
		slog.Error("rotate webhook secret", "error", err)
		return
	}

	respond.JSON(w, r, http.StatusOK, map[string]string{"secret": newSecret})
}

func (h *Handler) listEvents(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	events, err := h.svc.ListEvents(r.Context(), tenantID, 50)
	if err != nil {
		respond.InternalError(w, r)
		slog.Error("list webhook events", "error", err)
		return
	}
	if events == nil {
		events = []domain.WebhookEvent{}
	}

	respond.JSON(w, r, http.StatusOK, map[string]any{"data": events})
}

func (h *Handler) listDeliveries(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	eventID := chi.URLParam(r, "id")

	deliveries, err := h.svc.ListDeliveries(r.Context(), tenantID, eventID)
	if err != nil {
		respond.InternalError(w, r)
		slog.Error("list webhook deliveries", "error", err)
		return
	}
	if deliveries == nil {
		deliveries = []domain.WebhookDelivery{}
	}

	respond.JSON(w, r, http.StatusOK, map[string]any{"data": deliveries})
}

func (h *Handler) replayEvent(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	eventID := chi.URLParam(r, "id")

	err := h.svc.Replay(r.Context(), tenantID, eventID)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "webhook event")
		return
	}
	if err != nil {
		respond.InternalError(w, r)
		slog.Error("replay webhook event", "error", err)
		return
	}

	respond.JSON(w, r, http.StatusOK, map[string]string{"status": "replayed"})
}
