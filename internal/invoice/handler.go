package invoice

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
)

// CustomerGetter resolves customer IDs to names for PDF rendering.
type CustomerGetter interface {
	Get(ctx context.Context, tenantID, id string) (domain.Customer, error)
}

// SettingsGetter reads tenant settings for PDF company info.
type SettingsGetter interface {
	Get(ctx context.Context, tenantID string) (domain.TenantSettings, error)
}

// CreditNoteLister fetches credit notes for an invoice.
type CreditNoteLister interface {
	List(ctx context.Context, tenantID, invoiceID string) ([]domain.CreditNote, error)
}

type Handler struct {
	svc         *Service
	customers   CustomerGetter
	settings    SettingsGetter
	creditNotes CreditNoteLister
}

func NewHandler(svc *Service, customers CustomerGetter, settings SettingsGetter, creditNotes ...CreditNoteLister) *Handler {
	h := &Handler{svc: svc, customers: customers, settings: settings}
	if len(creditNotes) > 0 {
		h.creditNotes = creditNotes[0]
	}
	return h
}

func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/", h.create)
	r.Get("/", h.list)
	r.Get("/{id}", h.get)
	r.Get("/{id}/pdf", h.downloadPDF)
	r.Post("/{id}/finalize", h.finalize)
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

	inv, err := h.svc.Create(r.Context(), tenantID, input)
	if err != nil {
		respond.FromError(w, r, err, "invoice")
		return
	}

	respond.JSON(w, r, http.StatusCreated, inv)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	invoices, total, err := h.svc.List(r.Context(), ListFilter{
		TenantID:       tenantID,
		CustomerID:     r.URL.Query().Get("customer_id"),
		SubscriptionID: r.URL.Query().Get("subscription_id"),
		Status:         r.URL.Query().Get("status"),
		PaymentStatus:  r.URL.Query().Get("payment_status"),
		Limit:          limit,
		Offset:         offset,
	})
	if err != nil {
		respond.InternalError(w, r)
		slog.Error("list invoices", "error", err)
		return
	}
	if invoices == nil {
		invoices = []domain.Invoice{}
	}

	respond.List(w, r, invoices, total)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	inv, items, err := h.svc.GetWithLineItems(r.Context(), tenantID, id)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "invoice")
		return
	}
	if err != nil {
		respond.InternalError(w, r)
		slog.Error("get invoice", "error", err)
		return
	}
	if items == nil {
		items = []domain.InvoiceLineItem{}
	}

	respond.JSON(w, r, http.StatusOK, map[string]any{
		"invoice":    inv,
		"line_items": items,
	})
}

func (h *Handler) finalize(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	inv, err := h.svc.Finalize(r.Context(), tenantID, id)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "invoice")
		return
	}
	if err != nil {
		respond.Validation(w, r, err.Error())
		return
	}

	respond.JSON(w, r, http.StatusOK, inv)
}

func (h *Handler) void(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	inv, err := h.svc.Void(r.Context(), tenantID, id)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "invoice")
		return
	}
	if err != nil {
		respond.Validation(w, r, err.Error())
		return
	}

	respond.JSON(w, r, http.StatusOK, inv)
}

func (h *Handler) downloadPDF(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	inv, items, err := h.svc.GetWithLineItems(r.Context(), tenantID, id)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "invoice")
		return
	}
	if err != nil {
		respond.InternalError(w, r)
		return
	}

	customerName := inv.CustomerID
	if h.customers != nil {
		if cust, err := h.customers.Get(r.Context(), tenantID, inv.CustomerID); err == nil {
			customerName = cust.DisplayName
		}
	}

	var ci CompanyInfo
	if h.settings != nil {
		if ts, err := h.settings.Get(r.Context(), tenantID); err == nil {
			ci = CompanyInfo{
				Name:    ts.CompanyName,
				Email:   ts.CompanyEmail,
				Phone:   ts.CompanyPhone,
				Address: ts.CompanyAddress,
			}
		}
	}

	// Fetch credit notes for this invoice
	var cnInfos []CreditNoteInfo
	if h.creditNotes != nil {
		if notes, err := h.creditNotes.List(r.Context(), tenantID, id); err == nil {
			for _, cn := range notes {
				if cn.Status == domain.CreditNoteIssued {
					cnInfos = append(cnInfos, CreditNoteInfo{
						Number: cn.CreditNoteNumber,
						Reason: cn.Reason,
						Amount: cn.TotalCents,
					})
				}
			}
		}
	}

	pdfBytes, err := RenderPDF(inv, items, customerName, cnInfos, ci)
	if err != nil {
		respond.InternalError(w, r)
		return
	}

	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", "inline; filename=\""+inv.InvoiceNumber+".pdf\"")
	w.WriteHeader(http.StatusOK)
	w.Write(pdfBytes)
}
