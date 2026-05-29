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
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/audit"
	"github.com/sagarsuperuser/velox/internal/customerportal"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/invoice"
	"github.com/sagarsuperuser/velox/internal/subscription"
)

// InvoiceService is the slice of invoice operations the portal needs.
type InvoiceService interface {
	List(ctx context.Context, filter invoice.ListFilter) ([]domain.Invoice, int, error)
	Get(ctx context.Context, tenantID, id string) (domain.Invoice, error)
	GetWithLineItems(ctx context.Context, tenantID, id string) (domain.Invoice, []domain.InvoiceLineItem, error)
}

// SubscriptionService is the slice of subscription operations the portal needs.
type SubscriptionService interface {
	List(ctx context.Context, filter subscription.ListFilter) ([]domain.Subscription, int, error)
	Get(ctx context.Context, tenantID, id string) (domain.Subscription, error)
	// Cancel returns the canceled sub + the cents amount of any
	// cancel-proration credit granted (0 when none).
	Cancel(ctx context.Context, tenantID, id string) (domain.Subscription, int64, error)
	// ClearScheduledCancel undoes a prior cancel_at_period_end /
	// cancel_at schedule. Idempotent — a sub with no pending schedule
	// returns unchanged. Used by the portal "Resume" flow.
	ClearScheduledCancel(ctx context.Context, tenantID, id string) (domain.Subscription, error)
}

// CustomerGetter resolves customer and billing profile data for PDF rendering.
type CustomerGetter interface {
	Get(ctx context.Context, tenantID, id string) (domain.Customer, error)
	GetBillingProfile(ctx context.Context, tenantID, customerID string) (domain.CustomerBillingProfile, error)
}

// CustomerUpdater is the narrow surface the portal uses to let
// customers self-edit their own billing identity. Intentionally tighter
// than the operator customer.UpdateInput — only display_name + email
// are allow-listed. Status, dunning_policy_id, currency, tax_status,
// and other operator-controlled fields stay out of reach so a portal
// caller can't accidentally (or maliciously) reclassify their own
// account state.
type CustomerUpdater interface {
	UpdateProfile(ctx context.Context, tenantID, customerID string, displayName, email string) (domain.Customer, error)
}

// SettingsGetter reads tenant settings for PDF company info + portal branding.
type SettingsGetter interface {
	Get(ctx context.Context, tenantID string) (domain.TenantSettings, error)
}

// CreditNoteLister fetches issued credit notes to stamp onto invoice PDFs.
type CreditNoteLister interface {
	List(ctx context.Context, tenantID, invoiceID string) ([]domain.CreditNote, error)
}

// Payment methods are handled by the standalone internal/paymentmethods
// package, mounted in router.go at /v1/me/payment-methods (alongside
// this handler's /v1/me/* routes). That package already provides the
// full industry-grade surface (list, set-default, detach, setup-intent
// and setup-session, the last two being bootstrap-aware via
// paymentmethods.StripeAdapter.EnsureStripeCustomer). The portalapi
// Handler intentionally does NOT redo any PM work here — keeping the
// PM surface in one place avoids two divergent endpoints.

// InvoiceCharger triggers a real-money charge against the customer's
// default PM for a finalized-but-unpaid invoice. Used by the portal
// Pay-now flow. Implemented in production by payment.Stripe.
// Returns the updated invoice with payment_intent_id stamped (the
// charge is async; webhook reconciles final status).
type InvoiceCharger interface {
	ChargeInvoice(ctx context.Context, tenantID string, inv domain.Invoice, stripeCustomerID string) (domain.Invoice, error)
}

// PaymentSetupReader resolves the customer's default Stripe Customer
// ID + PM-presence flag. Required by Pay-now so we can reject early
// when the customer has no card on file (the alternative is a Stripe
// API error which is uglier to surface to the portal user).
type PaymentSetupReader interface {
	GetPaymentSetup(ctx context.Context, tenantID, customerID string) (domain.CustomerPaymentSetup, error)
}

// CreditReader is the narrow surface portalapi uses for the
// credit-balance card. Defined here (not imported from internal/credit)
// so customerportal doesn't gain a reverse dependency. Production
// implementation is *credit.Service via a one-line adapter in
// router.go.
type CreditReader interface {
	GetBalance(ctx context.Context, tenantID, customerID string) (domain.CreditBalance, error)
	ListEntries(ctx context.Context, filter CreditListFilter) ([]domain.CreditLedgerEntry, error)
}

// CreditListFilter mirrors the shape credit.ListFilter exposes —
// defined locally so the public-facing handler isn't dragging
// internal/credit's filter type into its dep graph.
type CreditListFilter struct {
	TenantID   string
	CustomerID string
	Limit      int
	Sort       string
	SortDir    string
}

type Handler struct {
	invoices       InvoiceService
	subscriptions  SubscriptionService
	customers      CustomerGetter
	customerWriter CustomerUpdater
	settings       SettingsGetter
	creditNotes    CreditNoteLister
	credits        CreditReader
	charger        InvoiceCharger
	pmSetup        PaymentSetupReader
	events         domain.EventDispatcher
	auditLogger    *audit.Logger
}

type Deps struct {
	Invoices       InvoiceService
	Subscriptions  SubscriptionService
	Customers      CustomerGetter
	CustomerWriter CustomerUpdater
	Settings       SettingsGetter
	CreditNotes    CreditNoteLister
	Credits        CreditReader
	Charger        InvoiceCharger
	PMSetup        PaymentSetupReader
	Events         domain.EventDispatcher
	AuditLogger    *audit.Logger
}

func New(deps Deps) *Handler {
	return &Handler{
		invoices:       deps.Invoices,
		subscriptions:  deps.Subscriptions,
		customers:      deps.Customers,
		customerWriter: deps.CustomerWriter,
		settings:       deps.Settings,
		creditNotes:    deps.CreditNotes,
		credits:        deps.Credits,
		charger:        deps.Charger,
		pmSetup:        deps.PMSetup,
		events:         deps.Events,
		auditLogger:    deps.AuditLogger,
	}
}

func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/profile", h.profile)
	// Self-edit: narrow allow-list (display_name + email only). All
	// operator-controlled fields (status, dunning policy, livemode)
	// stay locked. Industry parity: Stripe portal "Billing details"
	// edit allows name + email + address; tax-status / customer-tier
	// stay operator-only.
	r.Patch("/profile", h.updateProfile)
	r.Get("/invoices", h.listInvoices)
	r.Get("/invoices/{id}/pdf", h.downloadInvoicePDF)
	r.Get("/subscriptions", h.listSubscriptions)
	r.Post("/subscriptions/{id}/cancel", h.cancelSubscription)
	// Resume undoes a scheduled cancel (cancel_at_period_end=true).
	// Once a sub has actually canceled (status=canceled), it cannot
	// be resumed — that's a separate "reactivate" flow not yet in
	// the portal.
	r.Post("/subscriptions/{id}/resume", h.resumeSubscription)
	// Pay-now triggers an immediate charge against the customer's
	// default PM for a finalized-but-unpaid invoice. Industry parity
	// (Stripe portal "Pay invoice" button). Fails cleanly when no PM
	// on file or the invoice is already paid/voided.
	r.Post("/invoices/{id}/pay", h.payInvoice)
	r.Get("/credit-balance", h.creditBalance)
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
	ID               string `json:"id"`
	InvoiceNumber    string `json:"invoice_number"`
	Status           string `json:"status"`
	PaymentStatus    string `json:"payment_status"`
	Currency         string `json:"currency"`
	TotalAmountCents int64  `json:"total_amount_cents"`
	AmountDueCents   int64  `json:"amount_due_cents"`
	AmountPaidCents  int64  `json:"amount_paid_cents"`
	// CreditsAppliedCents is the portion of the invoice covered by the
	// customer's prepaid credit balance — distinct from amount_paid_cents
	// (which is only the PM-paid slice). Surfaced on the portal so a
	// customer who sees an invoice with `paid` status but `amount_paid_cents`
	// less than `total_amount_cents` can immediately see WHY their card
	// wasn't charged in full. Without this field, fully-credit-paid
	// invoices look confusingly like "paid but no payment" on the portal.
	CreditsAppliedCents int64      `json:"credits_applied_cents"`
	IssuedAt            *time.Time `json:"issued_at,omitempty"`
	DueAt               *time.Time `json:"due_at,omitempty"`
	PaidAt              *time.Time `json:"paid_at,omitempty"`
}

func toPortalInvoice(inv domain.Invoice) portalInvoice {
	return portalInvoice{
		ID:                  inv.ID,
		InvoiceNumber:       inv.InvoiceNumber,
		Status:              string(inv.Status),
		PaymentStatus:       string(inv.PaymentStatus),
		Currency:            inv.Currency,
		TotalAmountCents:    inv.TotalAmountCents,
		AmountDueCents:      inv.AmountDueCents,
		AmountPaidCents:     inv.AmountPaidCents,
		CreditsAppliedCents: inv.CreditsAppliedCents,
		IssuedAt:            inv.IssuedAt,
		DueAt:               inv.DueAt,
		PaidAt:              inv.PaidAt,
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
	// Default 50, clamp to 100 — no-silent-fallbacks principle.
	if limit <= 0 {
		limit = 50
	} else if limit > 100 {
		limit = 100
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
			// bp.Email removed in migration 0100 — bill-to email tracks
			// customers.email (set above).
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
				TaxID:        ts.TaxID,
				TaxIDType:    invoice.SupplierTaxIDTypeFromCountry(ts.CompanyCountry),
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
					Number:               cn.CreditNoteNumber,
					Reason:               cn.Reason,
					Amount:               cn.TotalCents,
					RefundAmountCents:    cn.RefundAmountCents,
					CreditAmountCents:    cn.CreditAmountCents,
					OutOfBandAmountCents: cn.OutOfBandAmountCents,
					TaxAmountCents:       cn.TaxAmountCents,
					TaxTransactionID:     cn.TaxTransactionID,
					RefundStatus:         string(cn.RefundStatus),
				})
			}
		}
	}

	pdfBytes, err := invoice.RenderPDF(r.Context(), inv, items, bt, cnInfos, ci)
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

// ---- Pay-now ----

// payInvoice triggers an immediate charge against the customer's
// default PM for a finalized-but-unpaid invoice. Industry parity:
// Stripe portal "Pay invoice" button, Chargebee "Pay now". The
// charge is async on Stripe's side; this endpoint returns 202
// Accepted with the updated invoice (now carrying the
// PaymentIntentID), and the payment_intent.succeeded/failed
// webhook flips the final status. The caller polls or refreshes
// invoices to see resolution.
//
// Failure modes (all 4xx, none leak whether the resource exists):
//   - invoice not found (or belongs to another customer) → 404
//   - invoice already paid → 409 invoice_already_paid
//   - invoice voided / draft → 409 invalid_state
//   - no PM on file (or no Stripe Customer object yet) → 409
//     no_payment_method_on_file (operator-friendly: "add a card first")
//   - PaymentIntent already in flight (payment_status=processing) → 409
//   - Stripe API error → 502 (preserved from payment.Stripe.ChargeInvoice)
func (h *Handler) payInvoice(w http.ResponseWriter, r *http.Request) {
	tenantID, customerID, ok := identity(r)
	if !ok {
		respond.Unauthorized(w, r, "missing portal session context")
		return
	}
	if h.charger == nil || h.pmSetup == nil {
		respond.Error(w, r, http.StatusServiceUnavailable, "api_error",
			"stripe_unavailable",
			"pay-now is not available — Stripe is not configured for this mode")
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		respond.BadRequest(w, r, "missing invoice id")
		return
	}
	inv, err := h.invoices.Get(r.Context(), tenantID, id)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "invoice")
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "portal pay invoice fetch", "error", err)
		respond.InternalError(w, r)
		return
	}
	// Cross-customer guard — see downloadInvoicePDF.
	if inv.CustomerID != customerID {
		respond.NotFound(w, r, "invoice")
		return
	}
	// Pre-flight state checks. Stripe will reject these too but the
	// error is uglier; surfacing them here gives the portal cleaner
	// 409 codes the SPA can translate into a friendly toast.
	switch {
	case inv.PaymentStatus == domain.PaymentSucceeded:
		respond.Error(w, r, http.StatusConflict, "invalid_state",
			"invoice_already_paid", "This invoice has already been paid.")
		return
	case inv.PaymentStatus == domain.PaymentProcessing:
		respond.Error(w, r, http.StatusConflict, "invalid_state",
			"payment_in_flight",
			"A charge is already in flight on this invoice — wait for it to settle before retrying.")
		return
	case inv.Status != domain.InvoiceFinalized:
		respond.Error(w, r, http.StatusConflict, "invalid_state",
			"invoice_not_payable",
			"Only finalized invoices can be paid via the portal.")
		return
	}
	ps, err := h.pmSetup.GetPaymentSetup(r.Context(), tenantID, customerID)
	if err != nil || ps.StripeCustomerID == "" {
		respond.Error(w, r, http.StatusConflict, "invalid_state",
			"no_payment_method_on_file",
			"Add a payment method before paying this invoice.")
		return
	}
	if !ps.DefaultPaymentMethodPresent {
		respond.Error(w, r, http.StatusConflict, "invalid_state",
			"no_payment_method_on_file",
			"No default payment method is set. Add or set a default card before paying.")
		return
	}
	charged, err := h.charger.ChargeInvoice(r.Context(), tenantID, inv, ps.StripeCustomerID)
	if err != nil {
		slog.ErrorContext(r.Context(), "portal pay invoice", "invoice_id", id, "error", err)
		respond.FromError(w, r, err, "invoice")
		return
	}
	// Audit the customer-initiated charge. The PaymentIntent confirmation
	// is async; the row marks the attempt so support can correlate it
	// with the eventual payment_intent.succeeded/failed webhook on the
	// AuditLog timeline.
	if h.auditLogger != nil {
		_ = h.auditLogger.Log(r.Context(), tenantID, domain.AuditActionUpdate, "invoice", charged.ID, charged.InvoiceNumber, map[string]any{
			"action":      "portal_pay_attempted",
			"customer_id": customerID,
			"amount_due":  inv.AmountDueCents,
		})
	}
	// 202 Accepted — the charge is async; webhook reconciles the
	// terminal state. The response carries the (now-processing)
	// invoice with the PaymentIntentID stamped so the SPA can
	// optimistic-update + poll.
	respond.JSON(w, r, http.StatusAccepted, toPortalInvoice(charged))
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
	// CancelAtPeriodEnd surfaces the scheduled-cancel state — true
	// means the sub is still active but will cancel at period end.
	// Drives the portal's "Cancel" vs "Resume" affordance: a
	// scheduled-cancel sub shows Resume (clears the schedule), a
	// regular active sub shows Cancel (sets the schedule).
	CancelAtPeriodEnd bool       `json:"cancel_at_period_end"`
	StartedAt         *time.Time `json:"started_at,omitempty"`
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
		CancelAtPeriodEnd:  sub.CancelAtPeriodEnd,
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
	// Default 50, clamp to 100 — no-silent-fallbacks principle.
	if limit <= 0 {
		limit = 50
	} else if limit > 100 {
		limit = 100
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

	canceled, _, err := h.subscriptions.Cancel(r.Context(), tenantID, id)
	if err != nil {
		respond.FromError(w, r, err, "subscription")
		return
	}

	// Audit log row tagged with actor_type='customer' (the auth ctx
	// carries the portal-session customer ID, picked up by audit.Logger).
	// Without this, the operator Activity feed and AuditLog page miss
	// every customer-portal-driven cancel — only the outbound webhook
	// reflects it. Operator-side Cancel does the equivalent write.
	if h.auditLogger != nil {
		_ = h.auditLogger.Log(r.Context(), tenantID, domain.AuditActionCancel, "subscription", canceled.ID, canceled.Code, map[string]any{
			"canceled_by": "customer",
			"customer_id": canceled.CustomerID,
		})
	}

	// Fire subscription.canceled webhook so partners see customer-initiated
	// cancels just like operator-initiated ones. The canceled_by field in
	// the payload tells them the cancel originated from the portal.
	h.fireSubscriptionEvent(r.Context(), tenantID, domain.EventSubscriptionCanceled, canceled, map[string]any{
		"canceled_by": "customer",
	})

	respond.JSON(w, r, http.StatusOK, toPortalSubscription(canceled))
}

// resumeSubscription clears a pending cancel_at_period_end / cancel_at
// schedule. The sub is still active (the schedule fires at period
// end); calling Resume before period end undoes the schedule and the
// sub keeps renewing. Industry parity (Stripe portal: "Renew
// subscription" button on a sub with cancel_at_period_end=true).
//
// 404 with the same shape as a wrong-customer attempt — no enumeration
// signal leaks for cross-customer sub IDs.
func (h *Handler) resumeSubscription(w http.ResponseWriter, r *http.Request) {
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
		slog.ErrorContext(r.Context(), "portal fetch subscription for resume", "error", err)
		respond.InternalError(w, r)
		return
	}
	if sub.CustomerID != customerID {
		respond.NotFound(w, r, "subscription")
		return
	}
	// A fully-canceled (status=canceled) sub can't be resumed via
	// this endpoint — that'd be a "reactivate" flow, which has
	// proration + billing-cycle reset implications. Stripe portal
	// has the same restriction; portals only un-schedule pending
	// cancels.
	if sub.Status == domain.SubscriptionCanceled {
		respond.Error(w, r, http.StatusConflict, "invalid_state",
			"subscription_canceled",
			"This subscription has already been canceled and can't be resumed from the portal. Contact support to reactivate.")
		return
	}
	resumed, err := h.subscriptions.ClearScheduledCancel(r.Context(), tenantID, id)
	if err != nil {
		respond.FromError(w, r, err, "subscription")
		return
	}
	// Mirror the operator-side clearScheduledCancel handler: write
	// AuditActionUpdate with sub-action='cancel_cleared' so the
	// subscription-activity timeline renders "Scheduled cancellation
	// cleared" for portal-driven resumes too. The describeSubscriptionAction
	// switch already handles this shape — no UI changes needed.
	if h.auditLogger != nil {
		_ = h.auditLogger.Log(r.Context(), tenantID, domain.AuditActionUpdate, "subscription", resumed.ID, resumed.Code, map[string]any{
			"action":      "cancel_cleared",
			"resumed_by":  "customer",
			"customer_id": resumed.CustomerID,
		})
	}
	h.fireSubscriptionEvent(r.Context(), tenantID, domain.EventSubscriptionCancelCleared, resumed, map[string]any{
		"resumed_by": "customer",
	})
	respond.JSON(w, r, http.StatusOK, toPortalSubscription(resumed))
}

// Payment-method routes used to live here; they were a duplicate of
// the bootstrap-aware /v1/me/payment-methods/* endpoints exposed by
// the internal/paymentmethods package (mounted alongside this handler
// in router.go). Removed to single-source the PM surface — the legacy
// /v1/me/payment-method/update endpoint required an existing Stripe
// Customer (operator-driven bootstrap), which broke true self-serve.
// The plural /v1/me/payment-methods/setup-session does the lazy
// EnsureStripeCustomer + Checkout-mode session in one call.

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

// ---- Profile ----

// profileResponse is the customer-safe projection of the customer
// record. Exposes display_name + email for the portal header and
// future "billing details" edit flow. Intentionally narrow — tax
// status, livemode, encrypted-PII raw bytes, blind-index columns
// stay server-side.
type profileResponse struct {
	CustomerID  string `json:"customer_id"`
	DisplayName string `json:"display_name,omitempty"`
	Email       string `json:"email,omitempty"`
}

// profile returns the authenticated customer's basic identity for
// portal header personalization ("Welcome, Acme Corp"). Cheap call
// — single PK lookup. Email + display_name decrypt at the store
// boundary so the wire shape is plaintext.
func (h *Handler) profile(w http.ResponseWriter, r *http.Request) {
	tenantID, customerID, ok := identity(r)
	if !ok {
		respond.Unauthorized(w, r, "missing portal session context")
		return
	}
	if h.customers == nil {
		respond.JSON(w, r, http.StatusOK, profileResponse{CustomerID: customerID})
		return
	}
	cust, err := h.customers.Get(r.Context(), tenantID, customerID)
	if err != nil {
		slog.ErrorContext(r.Context(), "portal profile fetch", "error", err)
		respond.InternalError(w, r)
		return
	}
	respond.JSON(w, r, http.StatusOK, profileResponse{
		CustomerID:  cust.ID,
		DisplayName: cust.DisplayName,
		Email:       cust.Email,
	})
}

type updateProfileRequest struct {
	DisplayName *string `json:"display_name,omitempty"`
	Email       *string `json:"email,omitempty"`
}

// updateProfile lets the customer self-edit their display_name and
// email. Both fields are pointers so the request can distinguish
// "omitted" (leave as-is) from "set to empty" (rejected — empty
// display_name / email is operator-only via Update, since it can
// break invoice delivery). Industry parity: Stripe portal lets the
// customer update billing contact email + name; Velox matches the
// narrow allow-list to that same surface.
//
// Validation rides on customer.Service.Update — empty values are
// ignored; bad emails return 422 with field=email; the rest of
// UpdateInput's fields (status, dunning policy) are never set by
// this path.
func (h *Handler) updateProfile(w http.ResponseWriter, r *http.Request) {
	tenantID, customerID, ok := identity(r)
	if !ok {
		respond.Unauthorized(w, r, "missing portal session context")
		return
	}
	if h.customerWriter == nil {
		respond.Error(w, r, http.StatusServiceUnavailable, "api_error",
			"profile_edit_unavailable",
			"profile editing is not available — customer writer not wired")
		return
	}
	var req updateProfileRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respond.BadRequest(w, r, "invalid JSON body")
			return
		}
	}
	// Both fields nil → no-op edit. Return current profile rather
	// than 4xx so the SPA's "save" without changes doesn't error.
	if req.DisplayName == nil && req.Email == nil {
		h.profile(w, r)
		return
	}
	var name, email string
	if req.DisplayName != nil {
		name = *req.DisplayName
	}
	if req.Email != nil {
		email = *req.Email
	}
	updated, err := h.customerWriter.UpdateProfile(r.Context(), tenantID, customerID, name, email)
	if err != nil {
		respond.FromError(w, r, err, "profile")
		return
	}
	// Profile edits are operator-visible: an email change can break
	// invoice delivery, a display-name change shows up on future PDFs.
	// Without the audit row the operator can't see who/when behind a
	// "why did this customer's email change?" support question.
	if h.auditLogger != nil {
		_ = h.auditLogger.Log(r.Context(), tenantID, domain.AuditActionUpdate, "customer", updated.ID, updated.DisplayName, map[string]any{
			"action":      "profile_updated",
			"customer_id": updated.ID,
			"updated_by":  "customer",
		})
	}
	respond.JSON(w, r, http.StatusOK, profileResponse{
		CustomerID:  updated.ID,
		DisplayName: updated.DisplayName,
		Email:       updated.Email,
	})
}

// ---- Credit balance ----

type creditLedgerEntry struct {
	ID          string     `json:"id"`
	EntryType   string     `json:"entry_type"`
	AmountCents int64      `json:"amount_cents"`
	Description string     `json:"description,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

type creditBalanceResponse struct {
	BalanceCents  int64               `json:"balance_cents"`
	TotalGranted  int64               `json:"total_granted"`
	TotalUsed     int64               `json:"total_used"`
	TotalExpired  int64               `json:"total_expired"`
	RecentEntries []creditLedgerEntry `json:"recent_entries"`
}

// creditBalance returns the customer's prepaid credit balance plus
// a short tail of ledger entries (most recent N) for transparency.
// Industry parity: Lago wallet view + Chargebee promo-credit history
// both surface balance + history on the portal.
//
// The list is intentionally short (capped at 10) — the portal is for
// at-a-glance review, not bulk export. Operators with API keys can
// fetch the full ledger via the operator endpoint.
func (h *Handler) creditBalance(w http.ResponseWriter, r *http.Request) {
	tenantID, customerID, ok := identity(r)
	if !ok {
		respond.Unauthorized(w, r, "missing portal session context")
		return
	}
	if h.credits == nil {
		// Credit service not wired — return empty balance shape so
		// the portal renders zero-state rather than 503.
		respond.JSON(w, r, http.StatusOK, creditBalanceResponse{
			RecentEntries: []creditLedgerEntry{},
		})
		return
	}
	bal, err := h.credits.GetBalance(r.Context(), tenantID, customerID)
	if err != nil {
		slog.ErrorContext(r.Context(), "portal credit balance", "error", err)
		respond.InternalError(w, r)
		return
	}
	entries, err := h.credits.ListEntries(r.Context(), CreditListFilter{
		TenantID:   tenantID,
		CustomerID: customerID,
		Limit:      10,
		Sort:       "created_at",
		SortDir:    "desc",
	})
	if err != nil {
		slog.ErrorContext(r.Context(), "portal credit ledger", "error", err)
		respond.InternalError(w, r)
		return
	}
	out := make([]creditLedgerEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, creditLedgerEntry{
			ID:          e.ID,
			EntryType:   string(e.EntryType),
			AmountCents: e.AmountCents,
			Description: e.Description,
			ExpiresAt:   e.ExpiresAt,
			CreatedAt:   e.CreatedAt,
		})
	}
	respond.JSON(w, r, http.StatusOK, creditBalanceResponse{
		BalanceCents:  bal.BalanceCents,
		TotalGranted:  bal.TotalGranted,
		TotalUsed:     bal.TotalUsed,
		TotalExpired:  bal.TotalExpired,
		RecentEntries: out,
	})
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
