package creditnote

import (
	"context"
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

// CustomerGetter resolves customer IDs to display names and billing profiles
// for the Bill-To block on the credit note PDF.
type CustomerGetter interface {
	Get(ctx context.Context, tenantID, id string) (domain.Customer, error)
	GetBillingProfile(ctx context.Context, tenantID, customerID string) (domain.CustomerBillingProfile, error)
}

// SettingsGetter reads tenant settings for the company header on the PDF.
type SettingsGetter interface {
	Get(ctx context.Context, tenantID string) (domain.TenantSettings, error)
}

// InvoiceGetter fetches the invoice the credit note was issued against.
// The CN PDF references the invoice number + date, tax country, tax name
// and reverse-charge flag — everything needed to keep the two documents
// legally consistent.
type InvoiceGetter interface {
	Get(ctx context.Context, tenantID, id string) (domain.Invoice, error)
}

// HandlerDeps bundles the PDF-only dependencies so the constructor stays
// small and callers can omit the block when they only need the JSON API.
type HandlerDeps struct {
	Customers CustomerGetter
	Settings  SettingsGetter
	Invoices  InvoiceGetter
}

type Handler struct {
	svc         *Service
	customers   CustomerGetter
	settings    SettingsGetter
	invoices    InvoiceGetter
	auditLogger *audit.Logger
}

func NewHandler(svc *Service, deps ...HandlerDeps) *Handler {
	h := &Handler{svc: svc}
	if len(deps) > 0 {
		h.customers = deps[0].Customers
		h.settings = deps[0].Settings
		h.invoices = deps[0].Invoices
	}
	return h
}

// SetAuditLogger configures audit logging for financial operations.
func (h *Handler) SetAuditLogger(l *audit.Logger) { h.auditLogger = l }

func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/", h.create)
	r.Get("/", h.list)
	r.Get("/{id}", h.get)
	r.Get("/{id}/pdf", h.downloadPDF)
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
		slog.ErrorContext(r.Context(), "list credit notes", "error", err)
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

func (h *Handler) downloadPDF(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	cn, items, err := h.svc.GetWithLineItems(r.Context(), tenantID, id)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "credit note")
		return
	}
	if err != nil {
		respond.InternalError(w, r)
		return
	}

	// Bill-To: prefer billing profile (legal name + full address for tax
	// compliance) and fall back to the customer record.
	bt := BillToInfo{Name: cn.CustomerID}
	if h.customers != nil {
		if cust, err := h.customers.Get(r.Context(), tenantID, cn.CustomerID); err == nil {
			bt.Name = cust.DisplayName
			bt.Email = cust.Email
		}
		if bp, err := h.customers.GetBillingProfile(r.Context(), tenantID, cn.CustomerID); err == nil {
			if bp.LegalName != "" {
				bt.Name = bp.LegalName
			}
			if bp.Email != "" {
				bt.Email = bp.Email
			}
			bt.AddressLine1 = bp.AddressLine1
			bt.AddressLine2 = bp.AddressLine2
			bt.City = bp.City
			bt.State = bp.State
			bt.PostalCode = bp.PostalCode
			bt.Country = bp.Country
			bt.TaxID = bp.TaxID
		}
	}

	// Company header from tenant settings.
	var ci CompanyInfo
	if h.settings != nil {
		if ts, err := h.settings.Get(r.Context(), tenantID); err == nil {
			ci = CompanyInfo{
				Name:         ts.CompanyName,
				Email:        ts.CompanyEmail,
				Phone:        ts.CompanyPhone,
				AddressLine1: ts.CompanyAddressLine1,
				AddressLine2: ts.CompanyAddressLine2,
				City:         ts.CompanyCity,
				State:        ts.CompanyState,
				PostalCode:   ts.CompanyPostalCode,
				Country:      ts.CompanyCountry,
				TaxID:        ts.TaxID,
			}
		}
	}

	// Original invoice reference. Absent the invoice getter we still render
	// the CN — the number lives on the CN itself, so only date/tax metadata
	// is lost in that path.
	var orig OriginalInvoiceInfo
	if h.invoices != nil {
		if inv, err := h.invoices.Get(r.Context(), tenantID, cn.InvoiceID); err == nil {
			orig = OriginalInvoiceInfo{
				Number:        inv.InvoiceNumber,
				IssuedAt:      inv.IssuedAt,
				Currency:      inv.Currency,
				TaxCountry:    inv.TaxCountry,
				TaxName:       inv.TaxName,
				TaxRateBP:     inv.TaxRateBP,
				ReverseCharge: inv.TaxReverseCharge,
				ExemptReason:  inv.TaxExemptReason,
			}
		}
	}

	pdfBytes, err := RenderPDF(cn, items, orig, bt, ci)
	if err != nil {
		slog.ErrorContext(r.Context(), "render credit note pdf", "credit_note_id", id, "error", err)
		respond.InternalError(w, r)
		return
	}

	filename := cn.CreditNoteNumber
	if filename == "" {
		filename = cn.ID
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", "inline; filename=\""+filename+".pdf\"")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pdfBytes)
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
