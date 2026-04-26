package bulkaction

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/auth"
	verrs "github.com/sagarsuperuser/velox/internal/errs"
)

// Handler exposes the operator-facing bulk action endpoints under
// /v1/admin/bulk_actions. Permissions are gated at the router mount
// point — both apply_coupon and schedule_cancel are write-grade because
// the cohort can be sensitive even when no DB mutation lands.
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// Routes registers the handler's sub-routes.
//
// Mounted at /v1/admin/bulk_actions:
//
//	POST /apply_coupon     — bulk attach coupon
//	POST /schedule_cancel  — bulk schedule cancel
//	GET  /                 — list past bulk actions (?status=&action_type=)
//	GET  /{id}             — detail with full errors[] array
func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/apply_coupon", h.applyCoupon)
	r.Post("/schedule_cancel", h.scheduleCancel)
	r.Get("/", h.list)
	r.Get("/{id}", h.get)
	return r
}

// --- Wire shapes --------------------------------------------------

type wireCustomerFilter struct {
	Type  string   `json:"type"`
	IDs   []string `json:"ids,omitempty"`
	Value string   `json:"value,omitempty"`
}

func (w wireCustomerFilter) toDomain() CustomerFilter {
	return CustomerFilter(w)
}

// wireApplyCouponRequest is POST /apply_coupon's input.
type wireApplyCouponRequest struct {
	IdempotencyKey string             `json:"idempotency_key"`
	CustomerFilter wireCustomerFilter `json:"customer_filter"`
	CouponCode     string             `json:"coupon_code"`
}

// wireScheduleCancelRequest is POST /schedule_cancel's input. Mirrors
// subscription.ScheduleCancelInput's two-mode shape (one of at_period_end
// or cancel_at must be set).
type wireScheduleCancelRequest struct {
	IdempotencyKey string             `json:"idempotency_key"`
	CustomerFilter wireCustomerFilter `json:"customer_filter"`
	AtPeriodEnd    bool               `json:"at_period_end,omitempty"`
	CancelAt       *time.Time         `json:"cancel_at,omitempty"`
}

// wireTargetError mirrors TargetError. Always-array shape on the
// containing wireCommitResponse.
type wireTargetError struct {
	CustomerID string `json:"customer_id"`
	Error      string `json:"error"`
}

// wireCommitResponse is the shared response for both apply_coupon and
// schedule_cancel commits. Same shape simplifies the dashboard's drawer
// — one component can render either action's outcome.
type wireCommitResponse struct {
	BulkActionID     string            `json:"bulk_action_id"`
	Status           string            `json:"status"`
	TargetCount      int               `json:"target_count"`
	SucceededCount   int               `json:"succeeded_count"`
	FailedCount      int               `json:"failed_count"`
	Errors           []wireTargetError `json:"errors"`
	IdempotentReplay bool              `json:"idempotent_replay,omitempty"`
}

// wireListItem is one row in GET /'s response.
type wireListItem struct {
	BulkActionID   string             `json:"bulk_action_id"`
	ActionType     string             `json:"action_type"`
	Status         string             `json:"status"`
	TargetCount    int                `json:"target_count"`
	SucceededCount int                `json:"succeeded_count"`
	FailedCount    int                `json:"failed_count"`
	CustomerFilter wireCustomerFilter `json:"customer_filter"`
	Params         map[string]any     `json:"params"`
	IdempotencyKey string             `json:"idempotency_key"`
	CreatedBy      string             `json:"created_by"`
	CreatedAt      string             `json:"created_at"`
	CompletedAt    string             `json:"completed_at,omitempty"`
}

// wireDetailItem is GET /{id}'s response — the list row plus the full
// errors[] array. List omits errors[] for payload size.
type wireDetailItem struct {
	wireListItem
	Errors []wireTargetError `json:"errors"`
}

// wireListResponse pins the list endpoint's response shape.
type wireListResponse struct {
	BulkActions []wireListItem `json:"bulk_actions"`
	NextCursor  string         `json:"next_cursor"`
}

// --- Marshalling helpers -----------------------------------------

func toWireCommitResponse(r CommitResult) wireCommitResponse {
	errs := make([]wireTargetError, 0, len(r.Errors))
	for _, e := range r.Errors {
		errs = append(errs, wireTargetError(e))
	}
	return wireCommitResponse{
		BulkActionID:     r.BulkActionID,
		Status:           r.Status,
		TargetCount:      r.TargetCount,
		SucceededCount:   r.SucceededCount,
		FailedCount:      r.FailedCount,
		Errors:           errs,
		IdempotentReplay: r.IdempotentReplay,
	}
}

func toWireListItem(a Action) wireListItem {
	completed := ""
	if a.CompletedAt != nil {
		completed = a.CompletedAt.UTC().Format(time.RFC3339)
	}
	params := a.Params
	if params == nil {
		params = map[string]any{}
	}
	return wireListItem{
		BulkActionID:   a.ID,
		ActionType:     a.ActionType,
		Status:         a.Status,
		TargetCount:    a.TargetCount,
		SucceededCount: a.SucceededCount,
		FailedCount:    a.FailedCount,
		CustomerFilter: wireCustomerFilter(a.CustomerFilter),
		Params:         params,
		IdempotencyKey: a.IdempotencyKey,
		CreatedBy:      a.CreatedBy,
		CreatedAt:      a.CreatedAt.UTC().Format(time.RFC3339),
		CompletedAt:    completed,
	}
}

func toWireDetailItem(a Action) wireDetailItem {
	errs := make([]wireTargetError, 0, len(a.Errors))
	for _, e := range a.Errors {
		errs = append(errs, wireTargetError(e))
	}
	return wireDetailItem{
		wireListItem: toWireListItem(a),
		Errors:       errs,
	}
}

// --- Handlers ---------------------------------------------------

func (h *Handler) applyCoupon(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	body, err := io.ReadAll(r.Body)
	if err != nil {
		respond.BadRequest(w, r, "could not read request body")
		return
	}
	var req wireApplyCouponRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			respond.BadRequest(w, r, "invalid JSON body")
			return
		}
	}

	actorID, actorType := actor(r)

	result, err := h.svc.ApplyCoupon(r.Context(), tenantID, ApplyCouponRequest{
		IdempotencyKey: req.IdempotencyKey,
		CustomerFilter: req.CustomerFilter.toDomain(),
		CouponCode:     req.CouponCode,
		CreatedBy:      actorID,
		CreatedByType:  actorType,
	})
	if err != nil {
		if errors.Is(err, verrs.ErrNotFound) {
			respond.NotFound(w, r, "bulk_action")
			return
		}
		respond.FromError(w, r, err, "bulk_action")
		return
	}

	respond.JSON(w, r, http.StatusOK, toWireCommitResponse(result))
}

func (h *Handler) scheduleCancel(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	body, err := io.ReadAll(r.Body)
	if err != nil {
		respond.BadRequest(w, r, "could not read request body")
		return
	}
	var req wireScheduleCancelRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			respond.BadRequest(w, r, "invalid JSON body")
			return
		}
	}

	actorID, actorType := actor(r)

	result, err := h.svc.ScheduleCancel(r.Context(), tenantID, ScheduleCancelRequest{
		IdempotencyKey: req.IdempotencyKey,
		CustomerFilter: req.CustomerFilter.toDomain(),
		AtPeriodEnd:    req.AtPeriodEnd,
		CancelAt:       req.CancelAt,
		CreatedBy:      actorID,
		CreatedByType:  actorType,
	})
	if err != nil {
		if errors.Is(err, verrs.ErrNotFound) {
			respond.NotFound(w, r, "bulk_action")
			return
		}
		respond.FromError(w, r, err, "bulk_action")
		return
	}

	respond.JSON(w, r, http.StatusOK, toWireCommitResponse(result))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	cursor := r.URL.Query().Get("cursor")
	status := r.URL.Query().Get("status")
	actionType := r.URL.Query().Get("action_type")

	rows, nextCursor, err := h.svc.List(r.Context(), tenantID, ListFilter{
		Status:     status,
		ActionType: actionType,
		Limit:      limit,
		Cursor:     cursor,
	})
	if err != nil {
		slog.ErrorContext(r.Context(), "list bulk actions", "error", err)
		respond.InternalError(w, r)
		return
	}

	items := make([]wireListItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, toWireListItem(row))
	}

	respond.JSON(w, r, http.StatusOK, wireListResponse{
		BulkActions: items,
		NextCursor:  nextCursor,
	})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	row, err := h.svc.Get(r.Context(), tenantID, id)
	if err != nil {
		if errors.Is(err, verrs.ErrNotFound) {
			respond.NotFound(w, r, "bulk_action")
			return
		}
		respond.FromError(w, r, err, "bulk_action")
		return
	}

	respond.JSON(w, r, http.StatusOK, toWireDetailItem(row))
}

// actor returns the (id, type) pair for the request's auth context.
// Empty key id (e.g. system caller) falls through to "system"/"system".
func actor(r *http.Request) (string, string) {
	id := auth.KeyID(r.Context())
	if id == "" {
		return "system", "system"
	}
	return id, "api_key"
}
