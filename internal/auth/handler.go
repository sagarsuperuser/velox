package auth

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/api/respond"
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
	r.Post("/{id}/rotate", h.rotate)
	return r
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	tenantID := TenantID(r.Context())
	if tenantID == "" {
		respond.Unauthorized(w, r, "not authenticated")
		return
	}

	var input CreateKeyInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}

	result, err := h.svc.CreateKey(r.Context(), tenantID, input)
	if err != nil {
		respond.FromError(w, r, err, "api_key")
		return
	}

	respond.JSON(w, r, http.StatusCreated, result)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	tenantID := TenantID(r.Context())
	if tenantID == "" {
		respond.Unauthorized(w, r, "not authenticated")
		return
	}

	keys, err := h.svc.ListKeys(r.Context(), ListFilter{TenantID: tenantID})
	if err != nil {
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(), "list api keys", "error", err)
		return
	}
	if keys == nil {
		keys = []domain.APIKey{}
	}

	respond.JSON(w, r, http.StatusOK, map[string]any{"data": keys})
}

func (h *Handler) revoke(w http.ResponseWriter, r *http.Request) {
	tenantID := TenantID(r.Context())
	id := chi.URLParam(r, "id")

	// Pre-ADR-011 guarded against revoking the calling Bearer key.
	// With user-bound dashboard sessions, revoking via the dashboard
	// doesn't kill the cookie session (sessions don't reference API
	// keys), so the foot-gun is gone. Bearer callers that revoke
	// their own key get an immediate 401 on the next request — that's
	// the operator's intent.

	key, err := h.svc.RevokeKey(r.Context(), tenantID, id)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "api key")
		return
	}
	if err != nil {
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(), "revoke api key", "error", err)
		return
	}

	respond.JSON(w, r, http.StatusOK, key)
}

func (h *Handler) rotate(w http.ResponseWriter, r *http.Request) {
	tenantID := TenantID(r.Context())
	id := chi.URLParam(r, "id")

	// Pre-ADR-011 guarded against self-rotation; with user-bound
	// dashboard sessions, rotating via the dashboard doesn't drop
	// the operator's auth (cookie isn't tied to the rotated key).
	// Bearer callers that rotate their own key need to swap to the
	// new raw_key in their config — that's the rotation contract.
	//
	// Body is optional — POST /rotate with no body defaults to immediate
	// revocation of the old key. An empty request should not 400.
	var input RotateKeyInput
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			respond.BadRequest(w, r, "invalid JSON body")
			return
		}
	}

	result, err := h.svc.RotateKey(r.Context(), tenantID, id, input)
	if err != nil {
		respond.FromError(w, r, err, "api_key")
		return
	}

	respond.JSON(w, r, http.StatusOK, result)
}
