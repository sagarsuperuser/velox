// Package hostedinvoice serves the public /v1/public/invoices/* surface —
// Stripe's hosted_invoice_url equivalent. The token in the URL is the sole
// credential: no API key, no session cookie. That matches the industry
// standard (Stripe, Lago, Orb, Paddle classic) so email CTAs from T0-16
// can link straight here without the end customer needing an account.
//
// Token resolution runs cross-tenant under TxBypass because the handler
// cannot set a tenant context before it knows which tenant the token
// belongs to. Cross-tenant probing isn't feasible: the token carries 256
// bits of entropy and the underlying column is UNIQUE indexed (see
// migration 0048). Once the invoice is resolved, every subsequent read
// uses that invoice's tenant — consistent with how portalapi scopes its
// /v1/me/* surface once the portal session resolves.
//
// Industry-standard semantics:
//   - Persistent URL: view remains accessible as long as the invoice
//     exists. Paid and voided invoices stay viewable (customers expect to
//     revisit past invoices for their records) but the Pay action is
//     disabled.
//   - Drafts never leak: a token can only be minted at finalize, and the
//     view returns 404 if the invoice ever regressed to draft.
//   - Safe projection of tenant branding only: company name, logo URL,
//     brand color, support URL, and registered-business address — enough
//     for customer-facing trust, nothing that could aid a cross-tenant
//     data probe. Tax IDs, invoice-numbering settings, tax configuration,
//     and internal flags stay hidden.
//   - Bill-to block mirrors what the PDF renders so the on-screen view
//     and the downloadable PDF match.
//   - Pay flow hands off to Stripe Checkout (mode=payment) for PCI-scope
//     offload and automatic wallet (Apple Pay / Google Pay) support.
//
// Dependencies are declared as narrow interfaces so the handler can be
// tested with in-memory fakes and coupling flows one way: hostedinvoice
// consumes invoice, customer, settings, credit-note, and a Stripe adapter,
// never the reverse.
package hostedinvoice

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/invoice"
)

// InvoiceResolver covers the invoice reads the hosted page needs. The
// GetByPublicToken call is intentionally un-scoped: it's the token lookup
// itself, used to discover which tenant owns the invoice.
type InvoiceResolver interface {
	GetByPublicToken(ctx context.Context, token string) (domain.Invoice, error)
	GetWithLineItems(ctx context.Context, tenantID, id string) (domain.Invoice, []domain.InvoiceLineItem, error)
}

// CustomerGetter resolves the bill-to block. Same shape as portalapi uses
// so both portals stay in step.
type CustomerGetter interface {
	Get(ctx context.Context, tenantID, id string) (domain.Customer, error)
	GetBillingProfile(ctx context.Context, tenantID, customerID string) (domain.CustomerBillingProfile, error)
}

// SettingsGetter reads the tenant's public-facing branding.
type SettingsGetter interface {
	Get(ctx context.Context, tenantID string) (domain.TenantSettings, error)
}

// CreditNoteLister is needed by the PDF render path: the PDF stamps
// applied credit notes onto the invoice so the customer can reconcile
// partial credits against the original amount.
type CreditNoteLister interface {
	List(ctx context.Context, tenantID, invoiceID string) ([]domain.CreditNote, error)
}

// CheckoutSessionCreator is the adapter the Pay button calls. The concrete
// implementation (in router.go) wraps *payment.StripeClients and handles
// the livemode lookup from the invoice row before picking the right Stripe
// key. Kept behind an interface so tests don't need a live Stripe client
// and so this package avoids importing internal/payment directly.
type CheckoutSessionCreator interface {
	CreateInvoicePaymentSession(ctx context.Context, tenantID string, inv domain.Invoice, successURL, cancelURL string) (string, error)
}

type Handler struct {
	invoices    InvoiceResolver
	customers   CustomerGetter
	settings    SettingsGetter
	creditNotes CreditNoteLister
	stripe      CheckoutSessionCreator
	// baseURL is the customer-facing SPA origin, e.g. https://app.velox.dev.
	// Used to build Stripe Checkout return URLs. If empty, the handler
	// falls back to localhost for dev. Kept as a config value rather than
	// hardcoded so single-tenant self-hosted deployments can point it at
	// their own branded domain without touching code.
	baseURL string
}

type Deps struct {
	Invoices    InvoiceResolver
	Customers   CustomerGetter
	Settings    SettingsGetter
	CreditNotes CreditNoteLister
	Stripe      CheckoutSessionCreator
	BaseURL     string
}

func New(deps Deps) *Handler {
	return &Handler{
		invoices:    deps.Invoices,
		customers:   deps.Customers,
		settings:    deps.Settings,
		creditNotes: deps.CreditNotes,
		stripe:      deps.Stripe,
		baseURL:     deps.BaseURL,
	}
}

func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/{token}", h.viewInvoice)
	r.Post("/{token}/checkout", h.createCheckoutSession)
	r.Get("/{token}/pdf", h.downloadPDF)
	return r
}

// ---- Response shapes ----

// hostedInvoicePayload is the safe, presentation-shaped projection the SPA
// consumes. Deliberately excludes internal IDs (subscription_id, stripe
// payment-intent id), Stripe metadata, and anything partner-operational.
// Customers see what they need to understand what they owe and what was
// delivered — nothing more.
type hostedInvoicePayload struct {
	Invoice   viewInvoice    `json:"invoice"`
	LineItems []viewLineItem `json:"line_items"`
	BillTo    viewBillTo     `json:"bill_to"`
	Branding  viewBranding   `json:"branding"`
	// PayEnabled is a convenience flag for the SPA. Equivalent to
	// "invoice.status == finalized && amount_due > 0", but computed
	// server-side so the rule lives in one place.
	PayEnabled bool `json:"pay_enabled"`
}

type viewInvoice struct {
	InvoiceNumber       string     `json:"invoice_number"`
	Status              string     `json:"status"`
	PaymentStatus       string     `json:"payment_status"`
	Currency            string     `json:"currency"`
	SubtotalCents       int64      `json:"subtotal_cents"`
	DiscountCents       int64      `json:"discount_cents"`
	TaxAmountCents      int64      `json:"tax_amount_cents"`
	TaxRateBP           int64      `json:"tax_rate_bp"`
	TaxName             string     `json:"tax_name,omitempty"`
	TaxReverseCharge    bool       `json:"tax_reverse_charge,omitempty"`
	TotalAmountCents    int64      `json:"total_amount_cents"`
	AmountDueCents      int64      `json:"amount_due_cents"`
	AmountPaidCents     int64      `json:"amount_paid_cents"`
	CreditsAppliedCents int64      `json:"credits_applied_cents"`
	IssuedAt            *time.Time `json:"issued_at,omitempty"`
	DueAt               *time.Time `json:"due_at,omitempty"`
	PaidAt              *time.Time `json:"paid_at,omitempty"`
	VoidedAt            *time.Time `json:"voided_at,omitempty"`
	Memo                string     `json:"memo,omitempty"`
	Footer              string     `json:"footer,omitempty"`
}

type viewLineItem struct {
	Description      string `json:"description"`
	Quantity         int64  `json:"quantity"`
	UnitAmountCents  int64  `json:"unit_amount_cents"`
	AmountCents      int64  `json:"amount_cents"`
	TaxAmountCents   int64  `json:"tax_amount_cents,omitempty"`
	TotalAmountCents int64  `json:"total_amount_cents"`
	Currency         string `json:"currency"`
}

type viewBillTo struct {
	Name         string `json:"name,omitempty"`
	Email        string `json:"email,omitempty"`
	AddressLine1 string `json:"address_line1,omitempty"`
	AddressLine2 string `json:"address_line2,omitempty"`
	City         string `json:"city,omitempty"`
	State        string `json:"state,omitempty"`
	PostalCode   string `json:"postal_code,omitempty"`
	Country      string `json:"country,omitempty"`
}

type viewBranding struct {
	CompanyName         string `json:"company_name,omitempty"`
	CompanyEmail        string `json:"company_email,omitempty"`
	CompanyPhone        string `json:"company_phone,omitempty"`
	CompanyAddressLine1 string `json:"company_address_line1,omitempty"`
	CompanyAddressLine2 string `json:"company_address_line2,omitempty"`
	CompanyCity         string `json:"company_city,omitempty"`
	CompanyState        string `json:"company_state,omitempty"`
	CompanyPostalCode   string `json:"company_postal_code,omitempty"`
	CompanyCountry      string `json:"company_country,omitempty"`
	LogoURL             string `json:"logo_url,omitempty"`
	BrandColor          string `json:"brand_color,omitempty"`
	SupportURL          string `json:"support_url,omitempty"`
}

// ---- Handlers ----

// resolveInvoice is the shared head of every handler: token → invoice,
// with the draft-never-leaks guard. Returns (invoice, false) and has
// already written the response on error; callers just return.
func (h *Handler) resolveInvoice(w http.ResponseWriter, r *http.Request) (domain.Invoice, bool) {
	token := chi.URLParam(r, "token")
	if token == "" {
		respond.NotFound(w, r, "invoice")
		return domain.Invoice{}, false
	}
	inv, err := h.invoices.GetByPublicToken(r.Context(), token)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "invoice")
		return domain.Invoice{}, false
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "hostedinvoice: token resolve", "error", err)
		respond.InternalError(w, r)
		return domain.Invoice{}, false
	}
	// Drafts must never be reachable via a public URL. Finalize is the
	// only code path that mints a token, but belt-and-suspenders: even
	// if a future code change regressed an invoice to draft, we still
	// 404 here rather than leak draft state to the public.
	if inv.Status == domain.InvoiceDraft {
		respond.NotFound(w, r, "invoice")
		return domain.Invoice{}, false
	}
	return inv, true
}

func (h *Handler) viewInvoice(w http.ResponseWriter, r *http.Request) {
	inv, ok := h.resolveInvoice(w, r)
	if !ok {
		return
	}

	// Line items: needed by the SPA to render the itemization. Pull with
	// the invoice's own tenantID (not from request ctx, which has none).
	_, items, err := h.invoices.GetWithLineItems(r.Context(), inv.TenantID, inv.ID)
	if err != nil {
		slog.ErrorContext(r.Context(), "hostedinvoice: list line items",
			"invoice_id", inv.ID, "error", err)
		respond.InternalError(w, r)
		return
	}

	// Bill-to: same projection portalapi uses for the PDF, minus the
	// tenant_id leak. Billing profile falls back to customer display_name
	// + email when legal_name is unset, matching portalapi behavior.
	billTo := viewBillTo{}
	if h.customers != nil {
		if cust, err := h.customers.Get(r.Context(), inv.TenantID, inv.CustomerID); err == nil {
			billTo.Name = cust.DisplayName
			billTo.Email = cust.Email
		}
		if bp, err := h.customers.GetBillingProfile(r.Context(), inv.TenantID, inv.CustomerID); err == nil {
			if bp.LegalName != "" {
				billTo.Name = bp.LegalName
			}
			if bp.Email != "" {
				billTo.Email = bp.Email
			}
			billTo.AddressLine1 = bp.AddressLine1
			billTo.AddressLine2 = bp.AddressLine2
			billTo.City = bp.City
			billTo.State = bp.State
			billTo.PostalCode = bp.PostalCode
			billTo.Country = bp.Country
		}
	}

	branding := viewBranding{}
	if h.settings != nil {
		if ts, err := h.settings.Get(r.Context(), inv.TenantID); err == nil {
			branding = viewBranding{
				CompanyName:         ts.CompanyName,
				CompanyEmail:        ts.CompanyEmail,
				CompanyPhone:        ts.CompanyPhone,
				CompanyAddressLine1: ts.CompanyAddressLine1,
				CompanyAddressLine2: ts.CompanyAddressLine2,
				CompanyCity:         ts.CompanyCity,
				CompanyState:        ts.CompanyState,
				CompanyPostalCode:   ts.CompanyPostalCode,
				CompanyCountry:      ts.CompanyCountry,
				LogoURL:             ts.LogoURL,
				BrandColor:          ts.BrandColor,
				SupportURL:          ts.SupportURL,
			}
		}
	}

	respond.JSON(w, r, http.StatusOK, hostedInvoicePayload{
		Invoice:    toViewInvoice(inv),
		LineItems:  toViewLineItems(items),
		BillTo:     billTo,
		Branding:   branding,
		PayEnabled: payEnabled(inv),
	})
}

// createCheckoutSession is the Pay button target. Creates a Stripe Checkout
// Session in payment mode for the invoice's outstanding amount. Tax is
// already computed on the invoice (Velox owns the tax decision, not
// Stripe), so the session uses a pre-totaled price_data line item and
// does not enable Stripe's automatic_tax.
func (h *Handler) createCheckoutSession(w http.ResponseWriter, r *http.Request) {
	inv, ok := h.resolveInvoice(w, r)
	if !ok {
		return
	}
	if !payEnabled(inv) {
		// Clear error rather than 200-with-no-url: the SPA surfaces this
		// as the paid/voided banner, but a rogue direct POST deserves an
		// honest state code so integrators know the operation was rejected.
		respond.Error(w, r, http.StatusConflict, "invalid_request_error", "not_payable",
			"invoice is not in a payable state")
		return
	}
	if h.stripe == nil {
		// Deployed without Stripe (dev / staging without keys). Surface
		// a 503 rather than 500: the state is a known operator misconfig,
		// not a bug.
		respond.Error(w, r, http.StatusServiceUnavailable, "api_error", "stripe_unavailable",
			"payment processing is not configured")
		return
	}

	successURL, cancelURL := h.checkoutReturnURLs(inv.PublicToken)
	sessionURL, err := h.stripe.CreateInvoicePaymentSession(r.Context(), inv.TenantID, inv, successURL, cancelURL)
	if err != nil {
		slog.ErrorContext(r.Context(), "hostedinvoice: checkout session create",
			"invoice_id", inv.ID, "tenant_id", inv.TenantID, "error", err)
		respond.Error(w, r, http.StatusBadGateway, "api_error", "stripe_error",
			"failed to create checkout session")
		return
	}

	type response struct {
		URL string `json:"url"`
	}
	respond.JSON(w, r, http.StatusOK, response{URL: sessionURL})
}

func (h *Handler) downloadPDF(w http.ResponseWriter, r *http.Request) {
	inv, ok := h.resolveInvoice(w, r)
	if !ok {
		return
	}

	_, items, err := h.invoices.GetWithLineItems(r.Context(), inv.TenantID, inv.ID)
	if err != nil {
		slog.ErrorContext(r.Context(), "hostedinvoice: pdf line items",
			"invoice_id", inv.ID, "error", err)
		respond.InternalError(w, r)
		return
	}

	bt := invoice.BillToInfo{Name: inv.CustomerID}
	if h.customers != nil {
		if cust, err := h.customers.Get(r.Context(), inv.TenantID, inv.CustomerID); err == nil {
			bt.Name = cust.DisplayName
			bt.Email = cust.Email
		}
		if bp, err := h.customers.GetBillingProfile(r.Context(), inv.TenantID, inv.CustomerID); err == nil {
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
		if ts, err := h.settings.Get(r.Context(), inv.TenantID); err == nil {
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
		if notes, err := h.creditNotes.List(r.Context(), inv.TenantID, inv.ID); err == nil {
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
		slog.ErrorContext(r.Context(), "hostedinvoice: pdf render",
			"invoice_id", inv.ID, "error", err)
		respond.InternalError(w, r)
		return
	}

	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", "inline; filename=\""+inv.InvoiceNumber+".pdf\"")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pdfBytes)
}

// ---- Helpers ----

// checkoutReturnURLs builds the success and cancel redirects for Stripe
// Checkout. Both point back to the hosted invoice page so the customer
// lands on the same URL they came from. Success carries ?paid=1 as a UI
// hint — the authoritative payment state comes from the Stripe webhook,
// but the flag lets the SPA render the thank-you view immediately instead
// of waiting a beat for the status to refresh.
func (h *Handler) checkoutReturnURLs(token string) (successURL, cancelURL string) {
	base := h.baseURL
	if base == "" {
		base = "http://localhost:5173"
	}
	successURL = base + "/invoice/" + token + "?paid=1"
	cancelURL = base + "/invoice/" + token
	return successURL, cancelURL
}

// payEnabled encodes the "can the customer click Pay" rule in one place.
// Finalized invoices with a positive amount_due are payable. Paid invoices
// aren't (already settled). Voided invoices aren't (no longer owed).
// amount_due=0 isn't payable either — that's a credit-fully-covered
// invoice, not a Pay action.
func payEnabled(inv domain.Invoice) bool {
	return inv.Status == domain.InvoiceFinalized && inv.AmountDueCents > 0
}

func toViewInvoice(inv domain.Invoice) viewInvoice {
	return viewInvoice{
		InvoiceNumber:       inv.InvoiceNumber,
		Status:              string(inv.Status),
		PaymentStatus:       string(inv.PaymentStatus),
		Currency:            inv.Currency,
		SubtotalCents:       inv.SubtotalCents,
		DiscountCents:       inv.DiscountCents,
		TaxAmountCents:      inv.TaxAmountCents,
		TaxRateBP:           inv.TaxRateBP,
		TaxName:             inv.TaxName,
		TaxReverseCharge:    inv.TaxReverseCharge,
		TotalAmountCents:    inv.TotalAmountCents,
		AmountDueCents:      inv.AmountDueCents,
		AmountPaidCents:     inv.AmountPaidCents,
		CreditsAppliedCents: inv.CreditsAppliedCents,
		IssuedAt:            inv.IssuedAt,
		DueAt:               inv.DueAt,
		PaidAt:              inv.PaidAt,
		VoidedAt:            inv.VoidedAt,
		Memo:                inv.Memo,
		Footer:              inv.Footer,
	}
}

func toViewLineItems(items []domain.InvoiceLineItem) []viewLineItem {
	out := make([]viewLineItem, 0, len(items))
	for _, it := range items {
		out = append(out, viewLineItem{
			Description:      it.Description,
			Quantity:         it.Quantity,
			UnitAmountCents:  it.UnitAmountCents,
			AmountCents:      it.AmountCents,
			TaxAmountCents:   it.TaxAmountCents,
			TotalAmountCents: it.TotalAmountCents,
			Currency:         it.Currency,
		})
	}
	return out
}
