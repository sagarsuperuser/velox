package recipe

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

// Handler exposes the recipes HTTP surface. The handler is thin: it
// decodes path/body params, calls Service, and translates errors via
// respond.FromError. All business logic lives in Service so handlers
// stay safe to refactor.
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// Routes returns the chi sub-router rooted at /v1/recipes (the parent
// router mounts it). Endpoints:
//
//	GET    /                           — list recipes + per-tenant install state
//	GET    /{key}                      — get one recipe (registry only)
//	POST   /{key}/preview              — render with overrides, no DB
//	POST   /{key}/instantiate          — full graph build under one tx
//	GET    /instances                  — list instantiated recipes for tenant
//	DELETE /instances/{id}             — remove the instance row (no cascade)
func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/", h.list)
	r.Get("/instances", h.listInstances)
	r.Delete("/instances/{id}", h.uninstall)
	r.Get("/{key}", h.get)
	r.Post("/{key}/preview", h.preview)
	r.Post("/{key}/instantiate", h.instantiate)
	return r
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	items, err := h.svc.ListRecipes(r.Context(), tenantID)
	if err != nil {
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(), "list recipes", "error", err)
		return
	}
	respond.JSON(w, r, http.StatusOK, map[string]any{"data": items})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	rec, err := h.svc.GetRecipe(key)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "recipe")
		return
	}
	if err != nil {
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(), "get recipe", "error", err, "key", key)
		return
	}
	respond.JSON(w, r, http.StatusOK, rec)
}

// previewRequest is the POST /preview body. Overrides is open-ended JSON
// (the recipe's overridable schema constrains values); the service
// validates against the recipe before rendering.
type previewRequest struct {
	Overrides map[string]any `json:"overrides"`
}

func (h *Handler) preview(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	var req previewRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respond.BadRequest(w, r, "invalid JSON body")
			return
		}
	}
	rec, err := h.svc.Preview(r.Context(), key, req.Overrides)
	if err != nil {
		respond.FromError(w, r, err, "recipe")
		return
	}
	respond.JSON(w, r, http.StatusOK, rec)
}

// instantiateRequest is the POST /instantiate body. Force is reserved
// for v2 (Service rejects it with InvalidState in v1) — accepting the
// field now keeps the contract stable when force support lands.
type instantiateRequest struct {
	Overrides map[string]any `json:"overrides"`
	Force     bool           `json:"force,omitempty"`
}

func (h *Handler) instantiate(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	key := chi.URLParam(r, "key")
	var req instantiateRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respond.BadRequest(w, r, "invalid JSON body")
			return
		}
	}
	inst, err := h.svc.Instantiate(r.Context(), tenantID, key, req.Overrides, InstantiateOptions{
		Force:     req.Force,
		CreatedBy: auth.KeyID(r.Context()),
	})
	if err != nil {
		respond.FromError(w, r, err, "recipe")
		return
	}
	respond.JSON(w, r, http.StatusCreated, inst)
}

func (h *Handler) listInstances(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	instances, err := h.svc.ListInstances(r.Context(), tenantID)
	if err != nil {
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(), "list recipe instances", "error", err)
		return
	}
	if instances == nil {
		instances = []domain.RecipeInstance{}
	}
	respond.JSON(w, r, http.StatusOK, map[string]any{"data": instances})
}

func (h *Handler) uninstall(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")
	if err := h.svc.Uninstall(r.Context(), tenantID, id); err != nil {
		respond.FromError(w, r, err, "recipe instance")
		return
	}
	respond.JSON(w, r, http.StatusOK, map[string]string{"status": "uninstalled"})
}
