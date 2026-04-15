package feature

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/api/respond"
)

// Handler serves HTTP endpoints for feature flag management.
type Handler struct {
	svc *Service
}

// NewHandler creates a new feature flag HTTP handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// Routes returns a chi.Router with all feature flag routes.
func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/", h.list)
	r.Put("/{key}", h.setGlobal)
	r.Put("/{key}/overrides/{tenant_id}", h.setOverride)
	r.Delete("/{key}/overrides/{tenant_id}", h.removeOverride)
	return r
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	flags, err := h.svc.List(r.Context())
	if err != nil {
		slog.Error("feature flags: list", "error", err)
		respond.InternalError(w, r)
		return
	}
	if flags == nil {
		flags = []Flag{}
	}
	respond.JSON(w, r, http.StatusOK, map[string]any{"data": flags})
}

type setGlobalRequest struct {
	Enabled bool `json:"enabled"`
}

func (h *Handler) setGlobal(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")

	var req setGlobalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}

	if err := h.svc.SetGlobal(r.Context(), key, req.Enabled); err != nil {
		slog.Error("feature flags: set global", "key", key, "error", err)
		respond.NotFound(w, r, "feature flag")
		return
	}

	respond.JSON(w, r, http.StatusOK, map[string]any{"key": key, "enabled": req.Enabled})
}

type setOverrideRequest struct {
	Enabled bool `json:"enabled"`
}

func (h *Handler) setOverride(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	tenantID := chi.URLParam(r, "tenant_id")

	var req setOverrideRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}

	if err := h.svc.SetOverride(r.Context(), tenantID, key, req.Enabled); err != nil {
		slog.Error("feature flags: set override", "key", key, "tenant", tenantID, "error", err)
		respond.InternalError(w, r)
		return
	}

	respond.JSON(w, r, http.StatusOK, map[string]any{
		"flag_key":  key,
		"tenant_id": tenantID,
		"enabled":   req.Enabled,
	})
}

func (h *Handler) removeOverride(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	tenantID := chi.URLParam(r, "tenant_id")

	if err := h.svc.RemoveOverride(r.Context(), tenantID, key); err != nil {
		slog.Error("feature flags: remove override", "key", key, "tenant", tenantID, "error", err)
		respond.InternalError(w, r)
		return
	}

	respond.JSON(w, r, http.StatusOK, map[string]any{"deleted": true})
}
