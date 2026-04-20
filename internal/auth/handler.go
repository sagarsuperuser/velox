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
		slog.ErrorContext(r.Context(),"list api keys", "error", err)
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

	// Guard: prevent revoking your own active key
	if id == KeyID(r.Context()) {
		respond.Error(w, r, http.StatusUnprocessableEntity, "invalid_request_error", "self_revoke", "cannot revoke the API key you are currently using")
		return
	}

	key, err := h.svc.RevokeKey(r.Context(), tenantID, id)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "api key")
		return
	}
	if err != nil {
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(),"revoke api key", "error", err)
		return
	}

	respond.JSON(w, r, http.StatusOK, key)
}

func (h *Handler) rotate(w http.ResponseWriter, r *http.Request) {
	tenantID := TenantID(r.Context())
	id := chi.URLParam(r, "id")

	// Self-rotation would drop the caller's authentication on the floor if
	// grace=0, and is a foot-gun even with a grace window — same reasoning
	// as the self-revoke guard.
	if id == KeyID(r.Context()) {
		respond.Error(w, r, http.StatusUnprocessableEntity, "invalid_request_error", "self_rotate",
			"cannot rotate the API key you are currently using; authenticate with a different key first")
		return
	}

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
