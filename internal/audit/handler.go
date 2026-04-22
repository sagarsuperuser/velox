package audit

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/domain"
)

type Handler struct {
	logger *Logger
}

func NewHandler(logger *Logger) *Handler {
	return &Handler{logger: logger}
}

func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/", h.list)
	r.Get("/filters", h.filters)
	return r
}

// filters returns the distinct action and resource_type values currently
// recorded for the tenant. The UI populates its filter dropdowns from this
// so new audit action types show up without a frontend release.
func (h *Handler) filters(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	actions, resourceTypes, err := h.logger.FilterOptions(r.Context(), tenantID)
	if err != nil {
		slog.ErrorContext(r.Context(), "audit filters", "error", err)
		respond.InternalError(w, r)
		return
	}
	if actions == nil {
		actions = []string{}
	}
	if resourceTypes == nil {
		resourceTypes = []string{}
	}
	respond.JSON(w, r, http.StatusOK, map[string]any{
		"actions":        actions,
		"resource_types": resourceTypes,
	})
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	entries, total, err := h.logger.Query(r.Context(), tenantID, QueryFilter{
		ResourceType: r.URL.Query().Get("resource_type"),
		ResourceID:   r.URL.Query().Get("resource_id"),
		Action:       r.URL.Query().Get("action"),
		ActorID:      r.URL.Query().Get("actor_id"),
		DateFrom:     r.URL.Query().Get("date_from"),
		DateTo:       r.URL.Query().Get("date_to"),
		Limit:        limit,
		Offset:       offset,
	})
	if err != nil {
		slog.ErrorContext(r.Context(), "list audit log", "error", err)
		respond.InternalError(w, r)
		return
	}
	if entries == nil {
		entries = []domain.AuditEntry{}
	}

	respond.List(w, r, entries, total)
}
