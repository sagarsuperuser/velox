package planmigration

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/billing"
	"github.com/sagarsuperuser/velox/internal/errs"
)

// Handler exposes the operator-facing plan migration endpoints under
// /v1/admin/plan_migrations. Permissions are gated at the router mount
// point (PermSubscriptionWrite for both preview and commit — both
// surfaces are write-grade because the cohort can be sensitive even
// when no DB mutation occurs).
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// Routes registers the handler's sub-routes. The literal routes
// (preview, commit) sit ahead of any future {id} param route so
// chi's pattern-precedence picks them first.
//
// Mounted at /v1/admin/plan_migrations:
//
//	POST /preview   — bulk preview across cohort
//	POST /commit    — apply migration
//	GET  /          — list past migrations
func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/preview", h.preview)
	r.Post("/commit", h.commit)
	r.Get("/", h.list)
	return r
}

// --- Wire shapes --------------------------------------------------

// wireCustomerFilter is the always-object shape clients send. type+value
// + ids cover all three filter modes; unrecognised types fall through to
// service-layer validation with a coded error.
type wireCustomerFilter struct {
	Type  string   `json:"type"`
	IDs   []string `json:"ids,omitempty"`
	Value string   `json:"value,omitempty"`
}

func (w wireCustomerFilter) toDomain() CustomerFilter {
	return CustomerFilter{
		Type:  w.Type,
		IDs:   w.IDs,
		Value: w.Value,
	}
}

// wirePreviewRequest is POST /preview's input.
type wirePreviewRequest struct {
	FromPlanID     string             `json:"from_plan_id"`
	ToPlanID       string             `json:"to_plan_id"`
	CustomerFilter wireCustomerFilter `json:"customer_filter"`
}

// wireCustomerPreview is one row in the preview response. before / after
// are inlined as the same shape billing.PreviewResult uses on the wire,
// so dashboards don't need to re-parse a custom envelope.
type wireCustomerPreview struct {
	CustomerID       string                `json:"customer_id"`
	CurrentPlanID    string                `json:"current_plan_id"`
	TargetPlanID     string                `json:"target_plan_id"`
	Before           billing.PreviewResult `json:"before"`
	After            billing.PreviewResult `json:"after"`
	DeltaAmountCents int64                 `json:"delta_amount_cents"`
	Currency         string                `json:"currency"`
}

// wirePreviewResponse pins the response shape for clients +
// wire-shape regression tests.
type wirePreviewResponse struct {
	Previews []wireCustomerPreview `json:"previews"`
	Totals   []wireMigrationTotal  `json:"totals"`
	Warnings []string              `json:"warnings"`
}

// wireMigrationTotal mirrors MigrationTotal but uses always-array shape
// at the response level (one entry per currency, even when there's
// only one).
type wireMigrationTotal struct {
	Currency          string `json:"currency"`
	BeforeAmountCents int64  `json:"before_amount_cents"`
	AfterAmountCents  int64  `json:"after_amount_cents"`
	DeltaAmountCents  int64  `json:"delta_amount_cents"`
}

// wireCommitRequest is POST /commit's input. idempotency_key is
// required; effective is enum-validated by the service layer.
type wireCommitRequest struct {
	FromPlanID     string             `json:"from_plan_id"`
	ToPlanID       string             `json:"to_plan_id"`
	CustomerFilter wireCustomerFilter `json:"customer_filter"`
	IdempotencyKey string             `json:"idempotency_key"`
	Effective      string             `json:"effective"`
}

// wireCommitResponse is POST /commit's output.
type wireCommitResponse struct {
	MigrationID      string `json:"migration_id"`
	AppliedCount     int    `json:"applied_count"`
	AuditLogID       string `json:"audit_log_id"`
	IdempotentReplay bool   `json:"idempotent_replay,omitempty"`
}

// wireMigrationListItem is one row in GET /'s response.
type wireMigrationListItem struct {
	MigrationID    string               `json:"migration_id"`
	FromPlanID     string               `json:"from_plan_id"`
	ToPlanID       string               `json:"to_plan_id"`
	Effective      string               `json:"effective"`
	AppliedAt      string               `json:"applied_at"`
	AppliedBy      string               `json:"applied_by"`
	AppliedByType  string               `json:"applied_by_type"`
	AppliedCount   int                  `json:"applied_count"`
	CustomerFilter wireCustomerFilter   `json:"customer_filter"`
	Totals         []wireMigrationTotal `json:"totals"`
	IdempotencyKey string               `json:"idempotency_key"`
	AuditLogID     string               `json:"audit_log_id,omitempty"`
}

// wireListResponse pins the list endpoint's response shape.
type wireListResponse struct {
	Migrations []wireMigrationListItem `json:"migrations"`
	NextCursor string                  `json:"next_cursor"`
}

// --- Marshalling helpers -----------------------------------------

func toWirePreviewResponse(r PreviewResult) wirePreviewResponse {
	previews := make([]wireCustomerPreview, 0, len(r.Previews))
	for _, p := range r.Previews {
		previews = append(previews, wireCustomerPreview{
			CustomerID:       p.CustomerID,
			CurrentPlanID:    p.CurrentPlanID,
			TargetPlanID:     p.TargetPlanID,
			Before:           p.Before,
			After:            p.After,
			DeltaAmountCents: p.DeltaAmountCents,
			Currency:         p.Currency,
		})
	}
	totals := make([]wireMigrationTotal, 0, len(r.Totals))
	for _, t := range r.Totals {
		totals = append(totals, wireMigrationTotal{
			Currency:          t.Currency,
			BeforeAmountCents: t.BeforeAmountCents,
			AfterAmountCents:  t.AfterAmountCents,
			DeltaAmountCents:  t.DeltaAmountCents,
		})
	}
	warnings := r.Warnings
	if warnings == nil {
		warnings = []string{}
	}
	return wirePreviewResponse{
		Previews: previews,
		Totals:   totals,
		Warnings: warnings,
	}
}

func toWireListItem(m Migration) wireMigrationListItem {
	totals := make([]wireMigrationTotal, 0, len(m.Totals))
	for _, t := range m.Totals {
		totals = append(totals, wireMigrationTotal{
			Currency:          t.Currency,
			BeforeAmountCents: t.BeforeAmountCents,
			AfterAmountCents:  t.AfterAmountCents,
			DeltaAmountCents:  t.DeltaAmountCents,
		})
	}
	return wireMigrationListItem{
		MigrationID:   m.ID,
		FromPlanID:    m.FromPlanID,
		ToPlanID:      m.ToPlanID,
		Effective:     m.Effective,
		AppliedAt:     m.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		AppliedBy:     m.AppliedBy,
		AppliedByType: m.AppliedByType,
		AppliedCount:  m.AppliedCount,
		CustomerFilter: wireCustomerFilter{
			Type:  m.CustomerFilter.Type,
			IDs:   m.CustomerFilter.IDs,
			Value: m.CustomerFilter.Value,
		},
		Totals:         totals,
		IdempotencyKey: m.IdempotencyKey,
		AuditLogID:     m.AuditLogID,
	}
}

// --- Handlers ---------------------------------------------------

func (h *Handler) preview(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	body, err := io.ReadAll(r.Body)
	if err != nil {
		respond.BadRequest(w, r, "could not read request body")
		return
	}
	var req wirePreviewRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			respond.BadRequest(w, r, "invalid JSON body")
			return
		}
	}

	result, err := h.svc.Preview(r.Context(), tenantID, PreviewRequest{
		FromPlanID:     req.FromPlanID,
		ToPlanID:       req.ToPlanID,
		CustomerFilter: req.CustomerFilter.toDomain(),
	})
	if err != nil {
		if errors.Is(err, errs.ErrNotFound) {
			respond.NotFound(w, r, "plan or customer")
			return
		}
		respond.FromError(w, r, err, "plan_migration")
		return
	}

	respond.JSON(w, r, http.StatusOK, toWirePreviewResponse(result))
}

func (h *Handler) commit(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	body, err := io.ReadAll(r.Body)
	if err != nil {
		respond.BadRequest(w, r, "could not read request body")
		return
	}
	var req wireCommitRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			respond.BadRequest(w, r, "invalid JSON body")
			return
		}
	}

	// Pull actor identity from auth context. Empty key id (e.g. system
	// caller) falls through to the service's "system" default.
	actorID := auth.KeyID(r.Context())
	actorType := "api_key"
	if actorID == "" {
		actorType = "system"
		actorID = "system"
	}

	result, err := h.svc.Commit(r.Context(), tenantID, CommitRequest{
		FromPlanID:     req.FromPlanID,
		ToPlanID:       req.ToPlanID,
		CustomerFilter: req.CustomerFilter.toDomain(),
		IdempotencyKey: req.IdempotencyKey,
		Effective:      req.Effective,
		AppliedBy:      actorID,
		AppliedByType:  actorType,
	})
	if err != nil {
		if errors.Is(err, errs.ErrNotFound) {
			respond.NotFound(w, r, "plan or customer")
			return
		}
		respond.FromError(w, r, err, "plan_migration")
		return
	}

	resp := wireCommitResponse{
		MigrationID:      result.MigrationID,
		AppliedCount:     result.AppliedCount,
		AuditLogID:       result.AuditLogID,
		IdempotentReplay: result.IdempotentReplay,
	}
	respond.JSON(w, r, http.StatusOK, resp)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	cursor := r.URL.Query().Get("cursor")

	rows, nextCursor, err := h.svc.List(r.Context(), tenantID, limit, cursor)
	if err != nil {
		slog.ErrorContext(r.Context(), "list plan migrations", "error", err)
		respond.InternalError(w, r)
		return
	}

	items := make([]wireMigrationListItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, toWireListItem(row))
	}

	respond.JSON(w, r, http.StatusOK, wireListResponse{
		Migrations: items,
		NextCursor: nextCursor,
	})
}
