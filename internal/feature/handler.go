package feature

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/auth"
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
//
// Gating differs by blast radius and is enforced per-route rather than at the
// mount point because a single mount-level permission can't distinguish the
// global mutation (affects every tenant) from the per-tenant override
// (affects only the caller's tenant), and the two are held by disjoint key
// types (platform vs secret/session):
//
//   - readGuard protects GET / — listing the global flags.
//   - globalGuard protects PUT /{key}, the global on/off switch. A global
//     flip changes behavior for ALL tenants, so it must be platform-only.
//   - tenantGuard protects the override routes, which are scoped to the
//     caller's own tenant (derived from auth context, never the URL).
//
// The override routes deliberately omit a {tenant_id} path segment: the
// tenant is taken from the authenticated principal so a tenant-scoped key
// cannot write or delete another tenant's override (IDOR).
func (h *Handler) Routes(readGuard, globalGuard, tenantGuard func(http.Handler) http.Handler) chi.Router {
	r := chi.NewRouter()
	r.With(readGuard).Get("/", h.list)
	r.With(globalGuard).Put("/{key}", h.setGlobal)
	r.With(tenantGuard).Put("/{key}/override", h.setOverride)
	r.With(tenantGuard).Delete("/{key}/override", h.removeOverride)
	return r
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	flags, err := h.svc.List(r.Context())
	if err != nil {
		slog.ErrorContext(r.Context(), "feature flags: list", "error", err)
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
		slog.ErrorContext(r.Context(), "feature flags: set global", "key", key, "error", err)
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
	// Tenant is the authenticated principal's, never a URL param — taking it
	// from the path let a tenant-scoped key write another tenant's override.
	tenantID := auth.TenantID(r.Context())

	var req setOverrideRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}

	if err := h.svc.SetOverride(r.Context(), tenantID, key, req.Enabled); err != nil {
		slog.ErrorContext(r.Context(), "feature flags: set override", "key", key, "tenant", tenantID, "error", err)
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
	// Tenant is the authenticated principal's, never a URL param — taking it
	// from the path let a tenant-scoped key delete another tenant's override.
	tenantID := auth.TenantID(r.Context())

	if err := h.svc.RemoveOverride(r.Context(), tenantID, key); err != nil {
		slog.ErrorContext(r.Context(), "feature flags: remove override", "key", key, "tenant", tenantID, "error", err)
		respond.InternalError(w, r)
		return
	}

	respond.JSON(w, r, http.StatusOK, map[string]any{"deleted": true})
}
