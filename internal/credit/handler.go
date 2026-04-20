package credit

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	mw "github.com/sagarsuperuser/velox/internal/api/middleware"
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
	r.Post("/grant", h.grant)
	r.Post("/adjust", h.adjust)
	r.Get("/balances", h.listBalances)
	r.Get("/balance/{customer_id}", h.getBalance)
	r.Get("/ledger/{customer_id}", h.listEntries)
	return r
}

func (h *Handler) grant(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	var input GrantInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}

	entry, err := h.svc.Grant(r.Context(), tenantID, input)
	if err != nil {
		respond.FromError(w, r, err, "credit")
		return
	}

	if h.auditLogger != nil {
		_ = h.auditLogger.Log(r.Context(), tenantID, domain.AuditActionGrant, "credit", entry.ID, map[string]any{
			"customer_id":  entry.CustomerID,
			"amount_cents": entry.AmountCents,
			"description":  entry.Description,
		})
	}
	mw.RecordCreditOperation("grant")

	respond.JSON(w, r, http.StatusCreated, entry)
}

func (h *Handler) adjust(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	var input AdjustInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}

	entry, err := h.svc.Adjust(r.Context(), tenantID, input)
	if err != nil {
		respond.FromError(w, r, err, "credit")
		return
	}

	if h.auditLogger != nil {
		action := "credit.adjustment"
		if entry.AmountCents < 0 {
			action = "credit.deduction"
		}
		_ = h.auditLogger.Log(r.Context(), tenantID, action, "credit", entry.ID, map[string]any{
			"customer_id":  entry.CustomerID,
			"amount_cents": entry.AmountCents,
			"description":  entry.Description,
		})
	}
	mw.RecordCreditOperation("adjustment")

	respond.JSON(w, r, http.StatusCreated, entry)
}

func (h *Handler) listBalances(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	balances, err := h.svc.ListBalances(r.Context(), tenantID)
	if err != nil {
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(), "list credit balances", "error", err)
		return
	}
	if balances == nil {
		balances = []domain.CreditBalance{}
	}

	respond.JSON(w, r, http.StatusOK, map[string]any{"data": balances})
}

func (h *Handler) getBalance(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	customerID := chi.URLParam(r, "customer_id")

	bal, err := h.svc.GetBalance(r.Context(), tenantID, customerID)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "customer")
		return
	}
	if err != nil {
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(), "get credit balance", "error", err)
		return
	}

	respond.JSON(w, r, http.StatusOK, bal)
}

func (h *Handler) listEntries(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	customerID := chi.URLParam(r, "customer_id")

	entries, err := h.svc.ListEntries(r.Context(), ListFilter{
		TenantID:   tenantID,
		CustomerID: customerID,
		EntryType:  r.URL.Query().Get("entry_type"),
	})
	if err != nil {
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(), "list credit entries", "error", err)
		return
	}
	if entries == nil {
		entries = []domain.CreditLedgerEntry{}
	}

	respond.JSON(w, r, http.StatusOK, map[string]any{"data": entries})
}
