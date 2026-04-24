// Package portalapi serves the /v1/me/* surface that customers use through
// the self-service portal. It sits behind customerportal.Middleware so every
// request carries a portal session identity (tenant_id + customer_id). The
// handler never reads tenant or customer from the request body — those come
// from ctx, and the handler scopes every list/fetch/mutate to them.
//
// Dependencies are declared as narrow interfaces so the handler can be tested
// with fakes, and so coupling flows one way: portalapi consumes invoice,
// subscription, customer, settings, credit-note, and webhook-event services,
// never the reverse.
package portalapi

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/customerportal"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/invoice"
	"github.com/sagarsuperuser/velox/internal/subscription"
)

// InvoiceService is the slice of invoice operations the portal needs.
type InvoiceService interface {
	List(ctx context.Context, filter invoice.ListFilter) ([]domain.Invoice, int, error)
	GetWithLineItems(ctx context.Context, tenantID, id string) (domain.Invoice, []domain.InvoiceLineItem, error)
}

// SubscriptionService is the slice of subscription operations the portal needs.
type SubscriptionService interface {
	List(ctx context.Context, filter subscription.ListFilter) ([]domain.Subscription, int, error)
	Get(ctx context.Context, tenantID, id string) (domain.Subscription, error)
	Cancel(ctx context.Context, tenantID, id string) (domain.Subscription, error)
}

// CustomerGetter resolves customer and billing profile data for PDF rendering.
type CustomerGetter interface {
	Get(ctx context.Context, tenantID, id string) (domain.Customer, error)
	GetBillingProfile(ctx context.Context, tenantID, customerID string) (domain.CustomerBillingProfile, error)
}

// SettingsGetter reads tenant settings for PDF company info + portal branding.
type SettingsGetter interface {
	Get(ctx context.Context, tenantID string) (domain.TenantSettings, error)
}

// CreditNoteLister fetches issued credit notes to stamp onto invoice PDFs.
type CreditNoteLister interface {
	List(ctx context.Context, tenantID, invoiceID string) ([]domain.CreditNote, error)
}

type Handler struct {
	invoices      InvoiceService
	subscriptions SubscriptionService
	customers     CustomerGetter
	settings      SettingsGetter
	creditNotes   CreditNoteLister
	events        domain.EventDispatcher
}

type Deps struct {
	Invoices      InvoiceService
	Subscriptions SubscriptionService
	Customers     CustomerGetter
	Settings      SettingsGetter
	CreditNotes   CreditNoteLister
	Events        domain.EventDispatcher
}

func New(deps Deps) *Handler {
	return &Handler{
		invoices:      deps.Invoices,
		subscriptions: deps.Subscriptions,
		customers:     deps.Customers,
		settings:      deps.Settings,
		creditNotes:   deps.CreditNotes,
		events:        deps.Events,
	}
}

func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/invoices", h.listInvoices)
	r.Get("/invoices/{id}/pdf", h.downloadInvoicePDF)
	r.Get("/subscriptions", h.listSubscriptions)
	r.Post("/subscriptions/{id}/cancel", h.cancelSubscription)
	r.Get("/branding", h.branding)
	return r
}

// identity pulls the portal-session context the middleware injected. If
// either field is empty the request bypassed Middleware — treat as auth
// failure rather than silently leak tenant-less writes.
func identity(r *http.Request) (tenantID, customerID string, ok bool) {
	tenantID = customerportal.TenantID(r.Context())
	customerID = customerportal.CustomerID(r.Context())
	return tenantID, customerID, tenantID != "" && customerID != ""
}

// ---- Invoices ----

type portalInvoice struct {
	ID               string     `json:"id"`
	InvoiceNumber    string     `json:"invoice_number"`
	Status           string     `json:"status"`
	PaymentStatus    string     `json:"payment_status"`
	Currency         string     `json:"currency"`
	TotalAmountCents int64      `json:"total_amount_cents"`
	AmountDueCents   int64      `json:"amount_due_cents"`
	AmountPaidCents  int64      `json:"amount_paid_cents"`
	IssuedAt         *time.Time `json:"issued_at,omitempty"`
	DueAt            *time.Time `json:"due_at,omitempty"`
	PaidAt           *time.Time `json:"paid_at,omitempty"`
}

func toPortalInvoice(inv domain.Invoice) portalInvoice {
	return portalInvoice{
		ID:               inv.ID,
		InvoiceNumber:    inv.InvoiceNumber,
		Status:           string(inv.Status),
		PaymentStatus:    string(inv.PaymentStatus),
		Currency:         inv.Currency,
		TotalAmountCents: inv.TotalAmountCents,
		AmountDueCents:   inv.AmountDueCents,
		AmountPaidCents:  inv.AmountPaidCents,
		IssuedAt:         inv.IssuedAt,
		DueAt:            inv.DueAt,
		PaidAt:           inv.PaidAt,
	}
}

func (h *Handler) listInvoices(w http.ResponseWriter, r *http.Request) {
	tenantID, customerID, ok := identity(r)
	if !ok {
		respond.Unauthorized(w, r, "missing portal session context")
		return
	}

	// Cap at 50 by default — portals are for humans, not batch exports.
	// Partners who need full history can fetch via their API key.
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}

	// Hide drafts from the customer — drafts are an internal state and
	// seeing one would invite confusion ("why is this invoice for $0?").
	// Operators can still view drafts via the /v1/invoices operator route.
	invs, total, err := h.invoices.List(r.Context(), invoice.ListFilter{
		TenantID:   tenantID,
		CustomerID: customerID,
		Limit:      limit,
		Offset:     offset,
	})
	if err != nil {
		slog.ErrorContext(r.Context(), "portal list invoices", "error", err)
		respond.InternalError(w, r)
		return
	}

	out := make([]portalInvoice, 0, len(invs))
	for _, inv := range invs {
		if inv.Status == domain.InvoiceDraft {
			continue
		}
		out = append(out, toPortalInvoice(inv))
	}
	respond.List(w, r, out, total)
}

func (h *Handler) downloadInvoicePDF(w http.ResponseWriter, r *http.Request) {
	tenantID, customerID, ok := identity(r)
	if !ok {
		respond.Unauthorized(w, r, "missing portal session context")
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		respond.BadRequest(w, r, "missing invoice id")
		return
	}

	inv, items, err := h.invoices.GetWithLineItems(r.Context(), tenantID, id)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "invoice")
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "portal fetch invoice for pdf", "error", err)
		respond.InternalError(w, r)
		return
	}

	// Cross-customer access check: the portal session authenticates customer
	// A, but the URL id could point to customer B's invoice under the same
	// tenant. 404 rather than 403 — we don't confirm the invoice exists.
	if inv.CustomerID != customerID {
		respond.NotFound(w, r, "invoice")
		return
	}
	// Draft invoices never leak to customers.
	if inv.Status == domain.InvoiceDraft {
		respond.NotFound(w, r, "invoice")
		return
	}

	bt := invoice.BillToInfo{Name: inv.CustomerID}
	if h.customers != nil {
		if cust, err := h.customers.Get(r.Context(), tenantID, inv.CustomerID); err == nil {
			bt.Name = cust.DisplayName
			bt.Email = cust.Email
		}
		if bp, err := h.customers.GetBillingProfile(r.Context(), tenantID, inv.CustomerID); err == nil {
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
		}
	}

	var ci invoice.CompanyInfo
	if h.settings != nil {
		if ts, err := h.settings.Get(r.Context(), tenantID); err == nil {
			ci = invoice.CompanyInfo{
				Name:         ts.CompanyName,
				Email:        ts.CompanyEmail,
				Phone:        ts.CompanyPhone,
				AddressLine1: ts.CompanyAddressLine1,
				AddressLine2: ts.CompanyAddressLine2,
				City:         ts.CompanyCity,
				State:        ts.CompanyState,
				PostalCode:   ts.CompanyPostalCode,
				Country:      ts.CompanyCountry,
				BrandColor:   ts.BrandColor,
			}
		}
	}

	var cnInfos []invoice.CreditNoteInfo
	if h.creditNotes != nil {
		if notes, err := h.creditNotes.List(r.Context(), tenantID, id); err == nil {
			for _, cn := range notes {
				if cn.Status != domain.CreditNoteIssued {
					continue
				}
				cnInfos = append(cnInfos, invoice.CreditNoteInfo{
					Number:            cn.CreditNoteNumber,
					Reason:            cn.Reason,
					Amount:            cn.TotalCents,
					RefundAmountCents: cn.RefundAmountCents,
					CreditAmountCents: cn.CreditAmountCents,
					RefundStatus:      string(cn.RefundStatus),
				})
			}
		}
	}

	pdfBytes, err := invoice.RenderPDF(inv, items, bt, cnInfos, ci)
	if err != nil {
		slog.ErrorContext(r.Context(), "portal pdf render", "error", err)
		respond.InternalError(w, r)
		return
	}

	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", "inline; filename=\""+inv.InvoiceNumber+".pdf\"")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pdfBytes)
}

// ---- Subscriptions ----

type portalSubscriptionItem struct {
	ID       string `json:"id"`
	PlanID   string `json:"plan_id"`
	Quantity int64  `json:"quantity"`
}

type portalSubscription struct {
	ID                 string                   `json:"id"`
	DisplayName        string                   `json:"display_name"`
	Status             string                   `json:"status"`
	Items              []portalSubscriptionItem `json:"items"`
	CurrentPeriodStart *time.Time               `json:"current_period_start,omitempty"`
	CurrentPeriodEnd   *time.Time               `json:"current_period_end,omitempty"`
	NextBillingAt      *time.Time               `json:"next_billing_at,omitempty"`
	TrialEndAt         *time.Time               `json:"trial_end_at,omitempty"`
	CanceledAt         *time.Time               `json:"canceled_at,omitempty"`
	StartedAt          *time.Time               `json:"started_at,omitempty"`
}

func toPortalSubscription(sub domain.Subscription) portalSubscription {
	items := make([]portalSubscriptionItem, 0, len(sub.Items))
	for _, it := range sub.Items {
		items = append(items, portalSubscriptionItem{
			ID:       it.ID,
			PlanID:   it.PlanID,
			Quantity: it.Quantity,
		})
	}
	return portalSubscription{
		ID:                 sub.ID,
		DisplayName:        sub.DisplayName,
		Status:             string(sub.Status),
		Items:              items,
		CurrentPeriodStart: sub.CurrentBillingPeriodStart,
		CurrentPeriodEnd:   sub.CurrentBillingPeriodEnd,
		NextBillingAt:      sub.NextBillingAt,
		TrialEndAt:         sub.TrialEndAt,
		CanceledAt:         sub.CanceledAt,
		StartedAt:          sub.StartedAt,
	}
}

func (h *Handler) listSubscriptions(w http.ResponseWriter, r *http.Request) {
	tenantID, customerID, ok := identity(r)
	if !ok {
		respond.Unauthorized(w, r, "missing portal session context")
		return
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}

	subs, total, err := h.subscriptions.List(r.Context(), subscription.ListFilter{
		TenantID:   tenantID,
		CustomerID: customerID,
		Limit:      limit,
		Offset:     offset,
	})
	if err != nil {
		slog.ErrorContext(r.Context(), "portal list subscriptions", "error", err)
		respond.InternalError(w, r)
		return
	}

	out := make([]portalSubscription, 0, len(subs))
	for _, sub := range subs {
		out = append(out, toPortalSubscription(sub))
	}
	respond.List(w, r, out, total)
}

func (h *Handler) cancelSubscription(w http.ResponseWriter, r *http.Request) {
	tenantID, customerID, ok := identity(r)
	if !ok {
		respond.Unauthorized(w, r, "missing portal session context")
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		respond.BadRequest(w, r, "missing subscription id")
		return
	}

	sub, err := h.subscriptions.Get(r.Context(), tenantID, id)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "subscription")
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "portal fetch subscription for cancel", "error", err)
		respond.InternalError(w, r)
		return
	}
	// Cross-customer guard — see downloadInvoicePDF. 404 not 403.
	if sub.CustomerID != customerID {
		respond.NotFound(w, r, "subscription")
		return
	}

	canceled, err := h.subscriptions.Cancel(r.Context(), tenantID, id)
	if err != nil {
		respond.FromError(w, r, err, "subscription")
		return
	}

	// Fire subscription.canceled webhook so partners see customer-initiated
	// cancels just like operator-initiated ones. The canceled_by field in
	// the payload tells them the cancel originated from the portal.
	h.fireSubscriptionEvent(r.Context(), tenantID, domain.EventSubscriptionCanceled, canceled, map[string]any{
		"canceled_by": "customer",
	})

	respond.JSON(w, r, http.StatusOK, toPortalSubscription(canceled))
}

func (h *Handler) fireSubscriptionEvent(ctx context.Context, tenantID, eventType string, sub domain.Subscription, extra map[string]any) {
	if h.events == nil {
		return
	}
	payload := map[string]any{
		"subscription_id": sub.ID,
		"customer_id":     sub.CustomerID,
		"status":          string(sub.Status),
		"item_count":      len(sub.Items),
	}
	if sub.CurrentBillingPeriodStart != nil {
		payload["current_period_start"] = sub.CurrentBillingPeriodStart.UTC()
	}
	if sub.CurrentBillingPeriodEnd != nil {
		payload["current_period_end"] = sub.CurrentBillingPeriodEnd.UTC()
	}
	maps.Copy(payload, extra)
	if err := h.events.Dispatch(ctx, tenantID, eventType, payload); err != nil {
		slog.ErrorContext(ctx, "dispatch portal subscription event",
			"event_type", eventType,
			"subscription_id", sub.ID,
			"tenant_id", tenantID,
			"error", err,
		)
	}
}

// ---- Branding ----

// brandingResponse is the safe projection of tenant settings exposed to
// customers. We deliberately don't include tax IDs, invoice prefixes, or
// any setting that could aid a cross-tenant data probe.
type brandingResponse struct {
	CompanyName string `json:"company_name,omitempty"`
	LogoURL     string `json:"logo_url,omitempty"`
	SupportURL  string `json:"support_url,omitempty"`
}

func (h *Handler) branding(w http.ResponseWriter, r *http.Request) {
	tenantID, _, ok := identity(r)
	if !ok {
		respond.Unauthorized(w, r, "missing portal session context")
		return
	}
	if h.settings == nil {
		respond.JSON(w, r, http.StatusOK, brandingResponse{})
		return
	}
	ts, err := h.settings.Get(r.Context(), tenantID)
	if err != nil {
		// A tenant without settings yet is a cold-start condition — return
		// an empty response, not 500. The portal UI falls back gracefully.
		if errors.Is(err, errs.ErrNotFound) {
			respond.JSON(w, r, http.StatusOK, brandingResponse{})
			return
		}
		slog.ErrorContext(r.Context(), "portal branding fetch", "error", err)
		respond.InternalError(w, r)
		return
	}
	respond.JSON(w, r, http.StatusOK, brandingResponse{
		CompanyName: ts.CompanyName,
		LogoURL:     ts.LogoURL,
		SupportURL:  ts.SupportURL,
	})
}
