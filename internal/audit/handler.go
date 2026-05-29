package audit

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/api/timefilter"
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

	dateFrom, dateTo, err := timefilter.ParseRange(r, "date_from", "date_to")
	if err != nil {
		respond.FromError(w, r, err, "audit_log")
		return
	}

	filter := QueryFilter{
		ResourceType: r.URL.Query().Get("resource_type"),
		ResourceID:   r.URL.Query().Get("resource_id"),
		Action:       r.URL.Query().Get("action"),
		ActorID:      r.URL.Query().Get("actor_id"),
		DateFrom:     dateFrom,
		DateTo:       dateTo,
		Limit:        limit,
		Offset:       offset,
	}
	// Cursor pagination (2026-05-29). ?after= takes precedence over
	// ?offset=. Malformed cursors silently fall back to offset.
	// Cursor encode/decode inlined here rather than imported from
	// internal/api/middleware because middleware/audit.go imports
	// this package (audit_log row-writer middleware) — circular dep
	// otherwise. The wire format is identical to middleware's
	// Cursor (base64(json{id, created_at})) so SPA clients can pass
	// audit cursors interchangeably with cursors from other endpoints.
	if c := r.URL.Query().Get("after"); c != "" {
		if cur, err := decodeAuditCursor(c); err == nil {
			filter.AfterCreatedAt = cur.CreatedAt
			filter.AfterID = cur.ID
		}
	}

	entries, total, err := h.logger.Query(r.Context(), tenantID, filter)
	if err != nil {
		slog.ErrorContext(r.Context(), "list audit log", "error", err)
		respond.InternalError(w, r)
		return
	}
	if entries == nil {
		entries = []domain.AuditEntry{}
	}

	if !filter.AfterCreatedAt.IsZero() && filter.AfterID != "" {
		l := filter.Limit
		if l <= 0 {
			l = 50
		} else if l > 100 {
			l = 100
		}
		hasMore := len(entries) > l
		if hasMore {
			entries = entries[:l]
		}
		resp := pageResponse{Data: entries, HasMore: hasMore}
		if hasMore && len(entries) > 0 {
			last := entries[len(entries)-1]
			resp.NextCursor = encodeAuditCursor(last.ID, last.CreatedAt)
		}
		respond.JSON(w, r, http.StatusOK, resp)
		return
	}

	respond.List(w, r, entries, total)
}

// Cursor pagination — mirrors the shape in
// internal/api/middleware/pagination.go (base64-encoded JSON of
// {id, created_at}). Duplicated here rather than imported to avoid the
// audit ↔ middleware import cycle. Wire-compatible with the
// middleware cursor shape so SPA clients can pass cursors from any
// list endpoint interchangeably.
type auditCursor struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
}

func encodeAuditCursor(id string, createdAt time.Time) string {
	c := auditCursor{ID: id, CreatedAt: createdAt}
	b, _ := json.Marshal(c)
	return base64.URLEncoding.EncodeToString(b)
}

func decodeAuditCursor(token string) (auditCursor, error) {
	if token == "" {
		return auditCursor{}, fmt.Errorf("empty cursor")
	}
	b, err := base64.URLEncoding.DecodeString(token)
	if err != nil {
		return auditCursor{}, fmt.Errorf("invalid cursor")
	}
	var c auditCursor
	if err := json.Unmarshal(b, &c); err != nil {
		return auditCursor{}, fmt.Errorf("invalid cursor")
	}
	return c, nil
}

type pageResponse struct {
	Data       any    `json:"data"`
	HasMore    bool   `json:"has_more"`
	NextCursor string `json:"next_cursor,omitempty"`
}
