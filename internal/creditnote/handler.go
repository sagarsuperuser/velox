package creditnote

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/api/middleware"
	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/audit"
	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
)

type Handler struct {
	svc         *Service
	auditLogger *audit.Logger
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// SetAuditLogger configures audit logging for financial operations.
func (h *Handler) SetAuditLogger(l *audit.Logger) { h.auditLogger = l }

func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/", h.create)
	r.Get("/", h.list)
	r.Get("/{id}", h.get)
	r.Post("/{id}/issue", h.issue)
	r.Post("/{id}/void", h.void)
	return r
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	var input CreateInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}

	cn, err := h.svc.Create(r.Context(), tenantID, input)
	if err != nil {
		respond.FromError(w, r, err, "credit_note")
		return
	}

	// Auto-issue if requested (create + issue in one call)
	if input.AutoIssue {
		issued, err := h.svc.Issue(r.Context(), tenantID, cn.ID)
		if err != nil {
			// CN was created but issue failed — void the draft to avoid orphans
			_, _ = h.svc.Void(r.Context(), tenantID, cn.ID)
			respond.FromError(w, r, err, "credit_note")
			return
		}
		h.auditLogCreditNote(r, tenantID, issued)
		respond.JSON(w, r, http.StatusCreated, issued)
		return
	}

	respond.JSON(w, r, http.StatusCreated, cn)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	page := middleware.ParsePageParams(r)

	cns, err := h.svc.List(r.Context(), ListFilter{
		TenantID:  tenantID,
		InvoiceID: r.URL.Query().Get("invoice_id"),
		Status:    r.URL.Query().Get("status"),
		Limit:     page.Limit,
		Offset:    page.Offset,
	})
	if err != nil {
		respond.InternalError(w, r)
		slog.Error("list credit notes", "error", err)
		return
	}
	if cns == nil {
		cns = []domain.CreditNote{}
	}

	respond.JSON(w, r, http.StatusOK, map[string]any{"data": cns})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	cn, err := h.svc.Get(r.Context(), tenantID, id)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "credit note")
		return
	}
	if err != nil {
		respond.InternalError(w, r)
		return
	}

	respond.JSON(w, r, http.StatusOK, cn)
}

func (h *Handler) issue(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	cn, err := h.svc.Issue(r.Context(), tenantID, id)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "credit note")
		return
	}
	if err != nil {
		respond.FromError(w, r, err, "credit_note")
		return
	}

	h.auditLogCreditNote(r, tenantID, cn)

	respond.JSON(w, r, http.StatusOK, cn)
}

func (h *Handler) void(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	cn, err := h.svc.Void(r.Context(), tenantID, id)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "credit note")
		return
	}
	if err != nil {
		respond.FromError(w, r, err, "credit_note")
		return
	}

	respond.JSON(w, r, http.StatusOK, cn)
}

// auditLogCreditNote logs a credit note issue event. Shared by issue and auto-issue paths.
func (h *Handler) auditLogCreditNote(r *http.Request, tenantID string, cn domain.CreditNote) {
	if h.auditLogger == nil {
		return
	}
	_ = h.auditLogger.Log(r.Context(), tenantID, "credit_note.issued", "credit_note", cn.ID, map[string]any{
		"credit_note_number":  cn.CreditNoteNumber,
		"invoice_id":          cn.InvoiceID,
		"customer_id":         cn.CustomerID,
		"total_cents":         cn.TotalCents,
		"refund_amount_cents": cn.RefundAmountCents,
		"credit_amount_cents": cn.CreditAmountCents,
		"reason":              cn.Reason,
		"currency":            cn.Currency,
	})
}
