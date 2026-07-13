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
	// Clocks the LOG has seen — including ones ADR-086 teardown has already
	// deleted, which are the ones a forensic query is usually about.
	clocks, err := h.logger.SimClocks(r.Context(), tenantID)
	if err != nil {
		slog.ErrorContext(r.Context(), "audit filters: sim clocks", "error", err)
		respond.InternalError(w, r)
		return
	}
	if actions == nil {
		actions = []string{}
	}
	if resourceTypes == nil {
		resourceTypes = []string{}
	}
	if clocks == nil {
		clocks = []SimClock{}
	}
	respond.JSON(w, r, http.StatusOK, map[string]any{
		"actions":        actions,
		"resource_types": resourceTypes,
		"test_clocks":    clocks,
	})
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	// Clamp, matching the limit convention below (Query clamps limit to
	// [50 default, 100 max]) — a negative offset previously reached
	// Postgres verbatim and surfaced as a 500.
	if offset < 0 {
		offset = 0
	}

	dateFrom, dateTo, err := timefilter.ParseRange(r, "date_from", "date_to")
	if err != nil {
		respond.FromError(w, r, err, "audit_log")
		return
	}
	// Sim-time window. Same parser as the wall-clock range (RFC3339 or bare
	// YYYY-MM-DD) — an operator windowing a simulation types dates, and the
	// dates they mean are the SIMULATED ones.
	simFrom, simTo, err := timefilter.ParseRange(r, "sim_from", "sim_to")
	if err != nil {
		respond.FromError(w, r, err, "audit_log")
		return
	}

	// There is ONE sort axis: created_at. `?order=` is still validated rather
	// than ignored, because a client that asks for an ordering it does not get,
	// and then pages through the result, reads rows in an order it is not
	// expecting. See QueryFilter for why ordering by simulated time does not
	// exist (short version: within a clock it is the same order; across clocks
	// it is a timeline that never happened).
	switch order := r.URL.Query().Get("order"); order {
	case "", "created_at":
	default:
		respond.BadRequest(w, r, "invalid `order` — audit rows are ordered by created_at")
		return
	}

	filter := QueryFilter{
		ResourceType: r.URL.Query().Get("resource_type"),
		ResourceID:   r.URL.Query().Get("resource_id"),
		Action:       r.URL.Query().Get("action"),
		ActorID:      r.URL.Query().Get("actor_id"),
		DateFrom:     dateFrom,
		DateTo:       dateTo,
		TestClockID:  r.URL.Query().Get("test_clock_id"),
		SimFrom:      simFrom,
		SimTo:        simTo,
		Limit:        limit,
		Offset:       offset,
	}
	// Cursor pagination (2026-05-29). ?after= takes precedence over
	// ?offset=. A malformed cursor is a 400, not a silent fallback — the
	// old fallback-to-offset restarted a paginating client at page 1,
	// which a CSV page-walk read as "export complete" (silent truncation
	// / duplication with zero error signal).
	// Cursor encode/decode inlined here rather than imported from
	// internal/api/middleware because middleware/audit.go imports
	// this package (audit_log row-writer middleware) — circular dep
	// otherwise. The wire format is identical to middleware's
	// Cursor (base64(json{id, created_at})) so SPA clients can pass
	// audit cursors interchangeably with cursors from other endpoints.
	if c := r.URL.Query().Get("after"); c != "" {
		cur, err := decodeAuditCursor(c)
		// A structurally-valid cursor with a zero id/timestamp would fall
		// through Query's useCursor check onto the offset path — the same
		// silent page-1 restart through the back door — so reject it too.
		if err != nil || cur.ID == "" || cur.CreatedAt.IsZero() {
			respond.BadRequest(w, r, "invalid `after` cursor — pass the next_cursor value from a previous page verbatim")
			return
		}
		filter.AfterCreatedAt = cur.CreatedAt
		filter.AfterID = cur.ID
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

	if usedCursor := !filter.AfterCreatedAt.IsZero(); usedCursor && filter.AfterID != "" {
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
			resp.NextCursor = encodeAuditCursor(entries[len(entries)-1])
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
// There is ONE sort axis (created_at, id), so there is ONE anchor. A second
// anchor existed here for an "order by simulated time" that is deliberately not
// shipped — see QueryFilter for why (within a clock it is the same order;
// across clocks it interleaves unrelated simulations).
type auditCursor struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
}

func encodeAuditCursor(e domain.AuditEntry) string {
	c := auditCursor{ID: e.ID, CreatedAt: e.CreatedAt}
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
