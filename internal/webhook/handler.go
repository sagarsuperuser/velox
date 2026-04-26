package webhook

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

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

// EventRoutes is the Week 6 real-time event surface, mounted at
// /v1/webhook_events. Lives here (rather than alongside Routes) so the
// route table stays tightly scoped to "the live-tail dashboard" and
// router.go can mount it under its own auth scope without dragging in
// the endpoint-management permissions.
//
// Critical: chi/v5 dispatches by registration order. We register
// /stream BEFORE /{id} so a literal "stream" path doesn't get captured
// as an ID and route to the deliveries handler. See
// docs/design-create-preview.md for the canonical write-up.
//
// Note: the SSE handler (streamEvents) IS exposed here for tests and
// dev-time mounting, but the production router mounts /stream
// SEPARATELY outside the /v1 block so it can skip the global 30s
// middleware.Timeout (which would kill any long-lived stream). See
// internal/api/router.go for the production mount.
func (h *Handler) EventRoutes() chi.Router {
	r := chi.NewRouter()
	r.Get("/stream", h.streamEvents)
	r.Get("/{id}/deliveries", h.listDeliveriesEnriched)
	r.Post("/{id}/replay", h.replayEventV2)
	return r
}

// StreamHandler is the chi-compatible handler for the live-tail SSE
// stream. Exported so router.go can mount it on a route block that
// skips the 30s timeout middleware applied to /v1/* routes.
func (h *Handler) StreamHandler() http.HandlerFunc {
	return h.streamEvents
}

func (h *Handler) getEndpointStats(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	stats, err := h.svc.GetEndpointStats(r.Context(), tenantID)
	if err != nil {
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(), "get webhook endpoint stats", "error", err)
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
		slog.ErrorContext(r.Context(), "list webhook endpoints", "error", err)
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

	result, err := h.svc.RotateSecret(r.Context(), tenantID, id)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "webhook endpoint")
		return
	}
	if err != nil {
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(), "rotate webhook secret", "error", err)
		return
	}

	respond.JSON(w, r, http.StatusOK, result)
}

func (h *Handler) listEvents(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	events, err := h.svc.ListEvents(r.Context(), tenantID, 50)
	if err != nil {
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(), "list webhook events", "error", err)
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
		slog.ErrorContext(r.Context(), "list webhook deliveries", "error", err)
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

	res, err := h.svc.Replay(r.Context(), tenantID, eventID)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "webhook event")
		return
	}
	if err != nil {
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(), "replay webhook event", "error", err)
		return
	}

	// Legacy path (under /v1/webhook-endpoints/events/{id}/replay): the
	// existing dashboard expects a flat {status: "replayed"} envelope
	// for backwards compatibility. The new Week 6 path returns the
	// richer {event_id, replay_of, status} struct via replayEventV2.
	respond.JSON(w, r, http.StatusOK, map[string]any{
		"status":    "replayed",
		"event_id":  res.EventID,
		"replay_of": res.ReplayOf,
	})
}

// replayEventV2 is the Week 6 dashboard-facing replay handler. Returns
// the full ReplayResult ({event_id, replay_of, status: "queued"}) so
// the dashboard can highlight the new clone in its live tail and toast
// the audit pivot.
func (h *Handler) replayEventV2(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	eventID := chi.URLParam(r, "id")

	res, err := h.svc.Replay(r.Context(), tenantID, eventID)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "webhook event")
		return
	}
	if err != nil {
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(), "replay webhook event v2", "error", err)
		return
	}
	respond.JSON(w, r, http.StatusOK, res)
}

// DeliveryView is the dashboard-facing delivery row: it carries the
// per-attempt facts the timeline needs (attempt number, status,
// response body, timestamps) plus the request_payload_sha256 the diff
// viewer uses to flag "payload identical between attempts" (the common
// case for Stripe-style replays).
//
// Snake_case throughout — pinned by TestWireShape_WebhookEventDeliveries.
type DeliveryView struct {
	ID                    string     `json:"id"`
	EventID               string     `json:"event_id"`
	EndpointID            string     `json:"endpoint_id"`
	AttemptNo             int        `json:"attempt_no"`
	Status                string     `json:"status"`
	StatusCode            int        `json:"status_code"`
	ResponseBody          string     `json:"response_body"`
	Error                 string     `json:"error"`
	RequestPayloadSHA256  string     `json:"request_payload_sha256"`
	AttemptedAt           time.Time  `json:"attempted_at"`
	CompletedAt           *time.Time `json:"completed_at"`
	NextRetryAt           *time.Time `json:"next_retry_at"`
	IsReplay              bool       `json:"is_replay"`
	ReplayEventID         string     `json:"replay_event_id"`
}

// DeliveriesResponse wraps the timeline. We surface root_event_id so
// the dashboard can confirm it received the original-pivot's chain
// (matters when the operator clicked Replay from a clone — the chain
// is still rooted at the original).
type DeliveriesResponse struct {
	RootEventID string         `json:"root_event_id"`
	Deliveries  []DeliveryView `json:"deliveries"`
}

// listDeliveriesEnriched is the Week 6 deliveries endpoint at GET
// /v1/webhook_events/{id}/deliveries. Differs from the legacy
// /v1/webhook-endpoints/events/{id}/deliveries by returning the
// enriched view shape (attempt_no, request_payload_sha256, etc.) and
// stitching the original event + every replay clone into one ordered
// timeline.
func (h *Handler) listDeliveriesEnriched(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	eventID := chi.URLParam(r, "id")

	// Resolve the root: an operator opening the dashboard after
	// clicking Replay on a clone might paste the clone's ID; the
	// timeline should still root at the original so the audit chain
	// shows the full history.
	root, err := h.svc.GetEvent(r.Context(), tenantID, eventID)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "webhook event")
		return
	}
	if err != nil {
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(), "list deliveries: get event", "error", err)
		return
	}
	rootID := root.ID
	if root.ReplayOfEventID != nil && *root.ReplayOfEventID != "" {
		rootID = *root.ReplayOfEventID
	}

	deliveries, err := h.svc.ListDeliveries(r.Context(), tenantID, rootID)
	if err != nil {
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(), "list deliveries", "error", err)
		return
	}

	// Pre-fetch every event in the tree once so we can hash their
	// payloads without re-querying per-delivery. The payload-SHA256
	// goes onto each delivery row for the diff viewer.
	tree := map[string]domain.WebhookEvent{rootID: {}}
	rootEv, _ := h.svc.GetEvent(r.Context(), tenantID, rootID)
	tree[rootID] = rootEv
	// Fetch each delivery's referencing event in one pass — uniqueified
	// by event_id since deliveries to multiple endpoints share an event.
	for _, d := range deliveries {
		if _, ok := tree[d.WebhookEventID]; !ok {
			ev, err := h.svc.GetEvent(r.Context(), tenantID, d.WebhookEventID)
			if err == nil {
				tree[d.WebhookEventID] = ev
			}
		}
	}

	out := make([]DeliveryView, 0, len(deliveries))
	for i, d := range deliveries {
		ev := tree[d.WebhookEventID]
		view := DeliveryView{
			ID:                   d.ID,
			EventID:              d.WebhookEventID,
			EndpointID:           d.WebhookEndpointID,
			AttemptNo:            i + 1,
			Status:               string(d.Status),
			StatusCode:           d.HTTPStatusCode,
			ResponseBody:         truncateBody(d.ResponseBody),
			Error:                d.ErrorMessage,
			RequestPayloadSHA256: hashPayload(ev.Payload),
			AttemptedAt:          d.CreatedAt,
			CompletedAt:          d.CompletedAt,
			NextRetryAt:          d.NextRetryAt,
		}
		if ev.ReplayOfEventID != nil && *ev.ReplayOfEventID != "" {
			view.IsReplay = true
			view.ReplayEventID = ev.ID
		}
		out = append(out, view)
	}

	respond.JSON(w, r, http.StatusOK, DeliveriesResponse{
		RootEventID: rootID,
		Deliveries:  out,
	})
}
