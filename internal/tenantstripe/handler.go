package tenantstripe

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

// Handler serves the tenant-facing Stripe connection endpoints. Mount under
// /v1/settings/stripe with auth middleware that populates the tenant ctx.
// This endpoint is deliberately platform-scoped: a tenant uses their single
// set of platform API keys to administer both modes via the same surface.
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/", h.connect)
	r.Get("/", h.list)
	r.Delete("/{mode}", h.delete)
	return r
}

func (h *Handler) connect(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	if tenantID == "" {
		respond.Unauthorized(w, r, "missing tenant context")
		return
	}

	var in ConnectInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}

	creds, err := h.svc.Connect(r.Context(), tenantID, in)
	if err != nil {
		respond.FromError(w, r, err, "stripe_credentials")
		return
	}
	respond.JSON(w, r, http.StatusOK, creds)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	if tenantID == "" {
		respond.Unauthorized(w, r, "missing tenant context")
		return
	}

	creds, err := h.svc.List(r.Context(), tenantID)
	if err != nil {
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(), "list stripe credentials", "error", err)
		return
	}
	if creds == nil {
		creds = []domain.StripeProviderCredentials{}
	}
	respond.JSON(w, r, http.StatusOK, map[string]any{"data": creds})
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	if tenantID == "" {
		respond.Unauthorized(w, r, "missing tenant context")
		return
	}

	mode := chi.URLParam(r, "mode")
	livemode, ok := parseMode(mode)
	if !ok {
		respond.BadRequest(w, r, `mode must be "test" or "live"`)
		return
	}

	if err := h.svc.Delete(r.Context(), tenantID, livemode); err != nil {
		if errors.Is(err, errs.ErrNotFound) {
			respond.NotFound(w, r, "stripe_credentials")
			return
		}
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(), "delete stripe credentials", "error", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func parseMode(mode string) (bool, bool) {
	switch mode {
	case "live":
		return true, true
	case "test":
		return false, true
	default:
		return false, false
	}
}
