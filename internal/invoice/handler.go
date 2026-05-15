package invoice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/audit"
	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
)

// CustomerGetter resolves customer IDs to names and billing profiles for PDF rendering.
type CustomerGetter interface {
	Get(ctx context.Context, tenantID, id string) (domain.Customer, error)
	GetBillingProfile(ctx context.Context, tenantID, customerID string) (domain.CustomerBillingProfile, error)
}

// SettingsGetter reads tenant settings for PDF company info.
type SettingsGetter interface {
	Get(ctx context.Context, tenantID string) (domain.TenantSettings, error)
}

// CreditNoteLister fetches credit notes for an invoice.
type CreditNoteLister interface {
	List(ctx context.Context, tenantID, invoiceID string) ([]domain.CreditNote, error)
}

// PaymentCharger creates a Stripe PaymentIntent for a finalized invoice.
type PaymentCharger interface {
	ChargeInvoice(ctx context.Context, tenantID string, inv domain.Invoice, stripeCustomerID string) (domain.Invoice, error)
}

// PaymentSetupGetter checks if a customer has a payment method ready.
type PaymentSetupGetter interface {
	GetPaymentSetup(ctx context.Context, tenantID, customerID string) (domain.CustomerPaymentSetup, error)
}

// CreditReverser returns credits to the customer when an invoice is voided.
type CreditReverser interface {
	ReverseForInvoice(ctx context.Context, tenantID, customerID, invoiceID, invoiceNumber string) (int64, error)
}

// PaymentCanceler cancels a Stripe PaymentIntent when an invoice is voided.
type PaymentCanceler interface {
	CancelPaymentIntent(ctx context.Context, paymentIntentID string) error
}

// BillingProfileGetter reads customer billing profile for PDF.
type BillingProfileGetter interface {
	GetBillingProfile(ctx context.Context, tenantID, customerID string) (domain.CustomerBillingProfile, error)
}

// DunningResolver resolves active dunning runs when an invoice is voided or paid.
type DunningResolver interface {
	ResolveByInvoice(ctx context.Context, tenantID, invoiceID string, resolution domain.DunningResolution) error
}

// WebhookEventLister lists webhook events for payment timeline.
type WebhookEventLister interface {
	ListByInvoice(ctx context.Context, tenantID, invoiceID string) ([]domain.StripeWebhookEvent, error)
}

// DunningTimelineFetcher fetches dunning data for payment timeline.
type DunningTimelineFetcher interface {
	ListRunsByInvoice(ctx context.Context, tenantID, invoiceID string) ([]domain.InvoiceDunningRun, error)
	ListEvents(ctx context.Context, tenantID, runID string) ([]domain.InvoiceDunningEvent, error)
}

// EmailSender sends invoice-related emails. ctx must carry livemode
// (set by auth middleware) so the underlying enqueue / brand lookup
// stamps the right tenant_settings + email_outbox row.
type EmailSender interface {
	SendInvoice(ctx context.Context, tenantID, to, customerName, invoiceNumber string, totalCents int64, currency string, pdfBytes []byte, publicToken string) error
}

// EmailEventLister surfaces customer-notification email rows
// (queued/dispatched/failed) tied to an invoice for the timeline.
// Without this, operators have no signal that the customer was
// notified — the no-PM finalize email goes out asynchronously and
// the only trace is the email_outbox row. Satisfied by
// email.OutboxStore.ListByInvoice.
type EmailEventLister interface {
	ListByInvoice(ctx context.Context, tenantID, invoiceNumber string) ([]EmailEventRow, error)
}

// EmailEventRow is the timeline-friendly view of an email_outbox row.
// Trimmed to the fields the timeline renderer needs.
type EmailEventRow struct {
	EmailType    string
	Status       string  // pending / dispatched / failed
	CreatedAt    time.Time
	DispatchedAt *time.Time
	LastError    string
	To           string  // resolved from payload
}

// RefundIssuer issues a direct refund on a paid invoice. Concretely this
// creates + issues a refund credit note atomically; the handler doesn't need
// to know about credit notes as a data model. Backed by creditnote.Service.
type RefundIssuer interface {
	IssueRefund(ctx context.Context, tenantID string, input RefundInput) (domain.CreditNote, error)
}

// RefundInput is the handler-facing refund request. AmountCents=0 means
// "refund the full remaining refundable amount".
type RefundInput struct {
	InvoiceID   string
	AmountCents int64
	Reason      string
	Description string
}

// validRefundReasons matches Stripe's refund reason enum plus "other" as the
// catch-all. Constrained at the edge so the UI can render a dropdown and the
// audit log has a stable vocabulary.
var validRefundReasons = map[string]bool{
	"duplicate":             true,
	"fraudulent":            true,
	"requested_by_customer": true,
	"other":                 true,
}

// getInvoiceServer is the slice of generated.ServerInterface that this
// handler currently implements — the single OpenAPI operation
// `getInvoice` (GET /v1/invoices/{id}). As more operations migrate
// onto the typed surface, this assertion will broaden until the
// handler conforms to the full generated.ServerInterface and the chi
// mount can swap to the generated route helper. The compile-time
// `var _` below catches any drift between the spec's signature and the
// handler's implementation as a build error rather than a runtime
// 404 — same trick Stripe-go and gh-cli use when they conform to
// generated SDK interfaces.
type getInvoiceServer interface {
	GetInvoice(w http.ResponseWriter, r *http.Request, id string)
}

var _ getInvoiceServer = (*Handler)(nil)

// SubscriptionClockReader is the narrow read interface used by the
// timeline composer to detect whether the invoice's owning sub is
// pinned to a test clock. When set, lifecycle + dunning events get
// `is_simulated=true` on the wire so the SPA can render the
// "simulated" chip authoritatively. Implemented by
// *subscription.PostgresStore.
type SubscriptionClockReader interface {
	Get(ctx context.Context, tenantID, id string) (domain.Subscription, error)
}

type Handler struct {
	svc             *Service
	customers       CustomerGetter
	settings        SettingsGetter
	creditNotes     CreditNoteLister
	charger         PaymentCharger
	paymentSetups   PaymentSetupGetter
	creditReverser  CreditReverser
	paymentCancel   PaymentCanceler
	dunning         DunningResolver
	webhookEvents   WebhookEventLister
	emailEvents     EmailEventLister
	dunningTimeline DunningTimelineFetcher
	subs            SubscriptionClockReader
	events          domain.EventDispatcher
	emailSender     EmailSender
	refundIssuer    RefundIssuer
	auditLogger     *audit.Logger
}

type HandlerDeps struct {
	CreditNotes     CreditNoteLister
	Charger         PaymentCharger
	PaymentSetups   PaymentSetupGetter
	CreditReverser  CreditReverser
	PaymentCancel   PaymentCanceler
	Dunning         DunningResolver
	WebhookEvents   WebhookEventLister
	EmailEvents     EmailEventLister
	DunningTimeline DunningTimelineFetcher
	Events          domain.EventDispatcher
	RefundIssuer    RefundIssuer
}

func NewHandler(svc *Service, customers CustomerGetter, settings SettingsGetter, deps ...HandlerDeps) *Handler {
	h := &Handler{svc: svc, customers: customers, settings: settings}
	if len(deps) > 0 {
		h.creditNotes = deps[0].CreditNotes
		h.charger = deps[0].Charger
		h.paymentSetups = deps[0].PaymentSetups
		h.creditReverser = deps[0].CreditReverser
		h.paymentCancel = deps[0].PaymentCancel
		h.dunning = deps[0].Dunning
		h.webhookEvents = deps[0].WebhookEvents
		h.emailEvents = deps[0].EmailEvents
		h.dunningTimeline = deps[0].DunningTimeline
		h.events = deps[0].Events
		h.refundIssuer = deps[0].RefundIssuer
	}
	return h
}

// SetEmailSender configures email sending for invoice notifications.
func (h *Handler) SetEmailSender(sender EmailSender) {
	h.emailSender = sender
}

// SetEmailEvents wires the email_outbox lister used by the timeline
// to surface customer-notification events. Optional — when nil, the
// timeline omits the email rows but the rest of it (lifecycle,
// stripe webhooks, dunning) renders unchanged.
func (h *Handler) SetEmailEvents(lister EmailEventLister) {
	h.emailEvents = lister
}

// SetSubscriptionClockReader wires the narrow sub reader used by the
// payment timeline to determine whether to stamp is_simulated=true on
// engine-driven events. Optional — when unwired, the timeline still
// renders but every event ships is_simulated=false (acceptable
// degraded behaviour for narrow tests; production always wires).
func (h *Handler) SetSubscriptionClockReader(r SubscriptionClockReader) {
	h.subs = r
}

// SetAuditLogger configures audit logging for financial operations.
func (h *Handler) SetAuditLogger(l *audit.Logger) { h.auditLogger = l }

// fireEvent dispatches a webhook event. Synchronous: with the outbox
// (RES-1) Dispatch is a short DB insert that must persist-before-return,
// and logging any failure is preferred to silently losing the event.
func (h *Handler) fireEvent(ctx context.Context, tenantID, eventType string, inv domain.Invoice) {
	if h.events == nil {
		return
	}
	if err := h.events.Dispatch(ctx, tenantID, eventType, map[string]any{
		"invoice_id":         inv.ID,
		"invoice_number":     inv.InvoiceNumber,
		"customer_id":        inv.CustomerID,
		"status":             string(inv.Status),
		"payment_status":     string(inv.PaymentStatus),
		"total_amount_cents": inv.TotalAmountCents,
		"amount_due_cents":   inv.AmountDueCents,
		"currency":           inv.Currency,
	}); err != nil {
		slog.ErrorContext(ctx, "dispatch invoice event",
			"event_type", eventType,
			"invoice_id", inv.ID,
			"tenant_id", tenantID,
			"error", err,
		)
	}
}

func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/", h.create)
	r.Get("/", h.list)
	r.Get("/{id}", h.get)
	r.Get("/{id}/pdf", h.downloadPDF)
	r.Post("/{id}/finalize", h.finalize)
	r.Post("/{id}/void", h.void)
	r.Post("/{id}/line-items", h.addLineItem)
	r.Post("/{id}/send", h.sendEmail)
	r.Post("/{id}/collect", h.collectPayment)
	r.Post("/{id}/refund", h.refund)
	r.Post("/{id}/apply-coupon", h.applyCoupon)
	r.Post("/{id}/retry-tax", h.retryTax)
	r.Post("/{id}/rotate-public-token", h.rotatePublicToken)
	r.Get("/{id}/payment-timeline", h.paymentTimeline)
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

func (h *Handler) addLineItem(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	var input AddLineItemInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}

	item, err := h.svc.AddLineItem(r.Context(), tenantID, id, input)
	if err != nil {
		respond.FromError(w, r, err, "invoice")
		return
	}

	respond.JSON(w, r, http.StatusCreated, item)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	// `ids` filter (comma-separated) lets CreditNotes-and-similar
	// pages fetch the exact invoices their primary rows reference,
	// avoiding the "list-then-client-side-join" pagination bug.
	var ids []string
	if raw := strings.TrimSpace(r.URL.Query().Get("ids")); raw != "" {
		for _, id := range strings.Split(raw, ",") {
			if id = strings.TrimSpace(id); id != "" {
				ids = append(ids, id)
			}
		}
	}

	invoices, total, err := h.svc.List(r.Context(), ListFilter{
		TenantID:       tenantID,
		CustomerID:     r.URL.Query().Get("customer_id"),
		SubscriptionID: r.URL.Query().Get("subscription_id"),
		Status:         r.URL.Query().Get("status"),
		PaymentStatus:  r.URL.Query().Get("payment_status"),
		IDs:            ids,
		Limit:          limit,
		Offset:         offset,
		// Sort + direction are validated against a closed set in
		// the store (invoiceOrderBy). Unknown sort keys silently
		// default to created_at; unknown dir defaults to desc.
		Sort:    r.URL.Query().Get("sort"),
		SortDir: r.URL.Query().Get("dir"),
	})
	if err != nil {
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(), "list invoices", "error", err)
		return
	}
	if invoices == nil {
		invoices = []domain.Invoice{}
	}

	respond.List(w, r, invoices, total)
}

// GetInvoice is the OpenAPI-typed handler for `GET /v1/invoices/{id}`.
// Signature matches generated.ServerInterface so the spec, the handler,
// and the router stay aligned at compile time — see the
// `var _ generated.GetInvoiceServer = (*Handler)(nil)` assertion below.
//
// The chi route still calls h.get (which extracts the id via chi.URLParam
// and delegates here), keeping the routing layer unchanged for now. As
// more endpoints adopt this pattern we'll switch the chi mount to use
// the generated route registration helper, which calls these typed
// methods directly.
func (h *Handler) GetInvoice(w http.ResponseWriter, r *http.Request, id string) {
	tenantID := auth.TenantID(r.Context())

	inv, items, err := h.svc.GetWithLineItems(r.Context(), tenantID, id)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "invoice")
		return
	}
	if err != nil {
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(), "get invoice", "error", err)
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

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	h.GetInvoice(w, r, chi.URLParam(r, "id"))
}

func (h *Handler) finalize(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	inv, err := h.svc.Finalize(r.Context(), tenantID, id)
	if err != nil {
		respond.FromError(w, r, err, "invoice")
		return
	}

	if h.auditLogger != nil {
		_ = h.auditLogger.Log(r.Context(), tenantID, domain.AuditActionFinalize, "invoice", inv.ID, map[string]any{
			"invoice_number":     inv.InvoiceNumber,
			"customer_id":        inv.CustomerID,
			"total_amount_cents": inv.TotalAmountCents,
			"currency":           inv.Currency,
		})
	}

	h.fireEvent(r.Context(), tenantID, domain.EventInvoiceFinalized, inv)

	// Send invoice email with PDF asynchronously.
	//
	// Bounded context (60s): if PDF render, DB reads, or SMTP send hangs,
	// the goroutine gives up and logs rather than leaking forever. The
	// invoice is already finalized — customers can still download the PDF
	// from the portal if email fails.
	//
	// context.WithoutCancel detaches from the request context so the email
	// job survives the HTTP handler returning.
	{
		emailCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 60*time.Second)
		go func() {
			defer cancel()
			cust, err := h.customers.Get(emailCtx, tenantID, inv.CustomerID)
			if err != nil || cust.Email == "" {
				slog.WarnContext(emailCtx, "skip invoice email — cannot resolve customer email",
					"invoice_id", inv.ID, "customer_id", inv.CustomerID, "error", err)
				return
			}
			email := cust.Email
			name := cust.DisplayName
			if bp, err := h.customers.GetBillingProfile(emailCtx, tenantID, inv.CustomerID); err == nil {
				if bp.Email != "" {
					email = bp.Email
				}
				if bp.LegalName != "" {
					name = bp.LegalName
				}
			}
			_, items, err := h.svc.GetWithLineItems(emailCtx, tenantID, inv.ID)
			if err != nil {
				slog.WarnContext(emailCtx, "skip invoice email — cannot fetch line items",
					"invoice_id", inv.ID, "error", err)
				return
			}
			// RenderPDF is CPU-bound and not ctx-aware. Check ctx before+after
			// so we don't waste a render when the deadline already passed, and
			// so we don't send a stale email if it did.
			if err := emailCtx.Err(); err != nil {
				slog.WarnContext(emailCtx, "skip invoice email — deadline reached before PDF render",
					"invoice_id", inv.ID, "error", err)
				return
			}
			bt := BillToInfo{Name: name, Email: email}
			pdfBytes, err := RenderPDF(emailCtx, inv, items, bt, nil, CompanyInfo{})
			if err != nil {
				slog.WarnContext(emailCtx, "skip invoice email — PDF render failed",
					"invoice_id", inv.ID, "error", err)
				return
			}
			if err := emailCtx.Err(); err != nil {
				slog.WarnContext(emailCtx, "skip invoice email — deadline reached after PDF render",
					"invoice_id", inv.ID, "error", err)
				return
			}
			if err := h.emailSender.SendInvoice(emailCtx, tenantID, email, name, inv.InvoiceNumber, inv.TotalAmountCents, inv.Currency, pdfBytes, inv.PublicToken); err != nil {
				slog.ErrorContext(emailCtx, "failed to send invoice email",
					"invoice_id", inv.ID, "email", email, "error", err)
			}
		}()
	}

	// Auto-charge: if customer has a payment method, create PaymentIntent
	if h.charger != nil && h.paymentSetups != nil && inv.AmountDueCents > 0 {
		if ps, err := h.paymentSetups.GetPaymentSetup(r.Context(), tenantID, inv.CustomerID); err == nil &&
			ps.SetupStatus == domain.PaymentSetupReady && ps.StripeCustomerID != "" {
			if charged, err := h.charger.ChargeInvoice(r.Context(), tenantID, inv, ps.StripeCustomerID); err != nil {
				slog.WarnContext(r.Context(), "auto-charge failed, invoice stays finalized",
					"invoice_id", inv.ID, "error", err)
			} else {
				inv = charged
				slog.InfoContext(r.Context(), "auto-charge initiated", "invoice_id", inv.ID)
			}
		}
	}

	respond.JSON(w, r, http.StatusOK, inv)
}

func (h *Handler) void(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	inv, err := h.svc.Void(r.Context(), tenantID, id)
	if err != nil {
		respond.FromError(w, r, err, "invoice")
		return
	}

	// Cancel Stripe PaymentIntent if one was created
	if h.paymentCancel != nil && inv.StripePaymentIntentID != "" {
		if err := h.paymentCancel.CancelPaymentIntent(r.Context(), inv.StripePaymentIntentID); err != nil {
			slog.WarnContext(r.Context(), "failed to cancel payment intent on void", "invoice_id", id, "pi_id", inv.StripePaymentIntentID, "error", err)
		} else {
			slog.InfoContext(r.Context(), "payment intent canceled on void", "invoice_id", id)
		}
	}

	// Reverse any credits that were applied to this invoice
	if h.creditReverser != nil && inv.CustomerID != "" {
		if reversed, err := h.creditReverser.ReverseForInvoice(r.Context(), tenantID, inv.CustomerID, id, inv.InvoiceNumber); err != nil {
			slog.WarnContext(r.Context(), "failed to reverse credits on void", "invoice_id", id, "error", err)
		} else if reversed > 0 {
			slog.InfoContext(r.Context(), "credits reversed on invoice void", "invoice_id", id, "reversed_cents", reversed)
		}
	}

	// Resolve any active dunning runs for this invoice
	if h.dunning != nil {
		if err := h.dunning.ResolveByInvoice(r.Context(), tenantID, id, domain.ResolutionManuallyResolved); err != nil {
			slog.WarnContext(r.Context(), "failed to resolve dunning on void", "invoice_id", id, "error", err)
		} else {
			slog.InfoContext(r.Context(), "dunning resolved on invoice void", "invoice_id", id)
		}
	}

	if h.auditLogger != nil {
		_ = h.auditLogger.Log(r.Context(), tenantID, domain.AuditActionVoid, "invoice", inv.ID, map[string]any{
			"invoice_number":     inv.InvoiceNumber,
			"customer_id":        inv.CustomerID,
			"total_amount_cents": inv.TotalAmountCents,
			"currency":           inv.Currency,
		})
	}

	h.fireEvent(r.Context(), tenantID, domain.EventInvoiceVoided, inv)

	respond.JSON(w, r, http.StatusOK, inv)
}

// rotatePublicToken mints a fresh hosted-invoice-URL token for an invoice,
// invalidating the previous one. Defensive rotation for the case where the
// public URL is ever shared where it shouldn't be (accidentally cc'd on a
// wider thread, pasted into a ticketing system, scraped from an email
// archive leak). Only finalized/paid/voided invoices carry tokens — draft
// invoices return 422, matching the store-level guard in SetPublicToken.
func (h *Handler) rotatePublicToken(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	inv, err := h.svc.Get(r.Context(), tenantID, id)
	if err != nil {
		respond.FromError(w, r, err, "invoice")
		return
	}
	if inv.Status == domain.InvoiceDraft {
		respond.Error(w, r, http.StatusUnprocessableEntity, "invalid_request_error", "invalid_state",
			"draft invoices do not have a public token — finalize first")
		return
	}

	previousToken := inv.PublicToken
	token, err := GeneratePublicToken()
	if err != nil {
		slog.ErrorContext(r.Context(), "rotate public token: generate", "invoice_id", id, "error", err)
		respond.InternalError(w, r)
		return
	}
	if err := h.svc.SetPublicToken(r.Context(), tenantID, id, token); err != nil {
		respond.FromError(w, r, err, "invoice")
		return
	}
	inv.PublicToken = token

	if h.auditLogger != nil {
		// Audit the rotation but NOT the token values themselves —
		// plaintext tokens in the audit log would turn the log into an
		// attractive target for credential harvesting. Record only that
		// a rotation happened.
		_ = h.auditLogger.Log(r.Context(), tenantID, domain.AuditActionRotate, "invoice", inv.ID, map[string]any{
			"invoice_number":           inv.InvoiceNumber,
			"customer_id":              inv.CustomerID,
			"field":                    "public_token",
			"previous_token_was_unset": previousToken == "",
		})
	}

	respond.JSON(w, r, http.StatusOK, inv)
}

func (h *Handler) sendEmail(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	var body struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Email == "" {
		respond.BadRequest(w, r, "email is required")
		return
	}

	inv, items, err := h.svc.GetWithLineItems(r.Context(), tenantID, id)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "invoice")
		return
	}
	if err != nil {
		respond.InternalError(w, r)
		return
	}

	// Build bill-to and company info for PDF
	bt := BillToInfo{Name: inv.CustomerID}
	if h.customers != nil {
		if cust, custErr := h.customers.Get(r.Context(), tenantID, inv.CustomerID); custErr == nil {
			bt.Name = cust.DisplayName
			bt.Email = cust.Email
		}
		if bp, bpErr := h.customers.GetBillingProfile(r.Context(), tenantID, inv.CustomerID); bpErr == nil {
			if bp.LegalName != "" {
				bt.Name = bp.LegalName
			}
		}
	}

	var ci CompanyInfo
	if h.settings != nil {
		if ts, tsErr := h.settings.Get(r.Context(), tenantID); tsErr == nil {
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
				BrandColor:   ts.BrandColor,
				TaxID:        ts.TaxID,
				TaxIDType:    SupplierTaxIDTypeFromCountry(ts.CompanyCountry),
			}
		}
	}

	pdfBytes, err := RenderPDF(r.Context(), inv, items, bt, nil, ci)
	if err != nil {
		respond.InternalError(w, r)
		return
	}

	if err := h.emailSender.SendInvoice(r.Context(), tenantID, body.Email, bt.Name, inv.InvoiceNumber, inv.TotalAmountCents, inv.Currency, pdfBytes, inv.PublicToken); err != nil {
		// Sanitize at the boundary — SMTP errors / outbox-store errors
		// would otherwise leak to the operator toast. ADR-026.
		respond.FromError(w, r, err, "invoice_email")
		return
	}

	respond.JSON(w, r, http.StatusOK, map[string]string{"status": "sent"})
}

func (h *Handler) collectPayment(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	inv, err := h.svc.Get(r.Context(), tenantID, id)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "invoice")
		return
	}
	if err != nil {
		respond.InternalError(w, r)
		return
	}

	if inv.Status != domain.InvoiceFinalized {
		respond.Validation(w, r, "can only collect payment on finalized invoices")
		return
	}
	if inv.PaymentStatus == domain.PaymentSucceeded {
		respond.Validation(w, r, "invoice is already paid")
		return
	}
	if inv.AmountDueCents <= 0 {
		respond.Validation(w, r, "invoice has no amount due")
		return
	}

	if h.charger == nil || h.paymentSetups == nil {
		respond.Validation(w, r, "payment provider not configured")
		return
	}

	ps, err := h.paymentSetups.GetPaymentSetup(r.Context(), tenantID, inv.CustomerID)
	if err != nil || ps.SetupStatus != domain.PaymentSetupReady || ps.StripeCustomerID == "" {
		respond.Validation(w, r, "customer has no payment method set up")
		return
	}

	charged, err := h.charger.ChargeInvoice(r.Context(), tenantID, inv, ps.StripeCustomerID)
	if err != nil {
		// ADR-026 boundary sanitization: ChargeInvoice wraps a
		// *payment.PaymentError which respond.FromError detects and
		// surfaces via OperatorSafeMessage() — humanized decline
		// reason or "Payment provider rejected the request" instead
		// of raw Stripe SDK strings (idempotency conflicts, etc.).
		respond.FromError(w, r, err, "payment")
		return
	}

	// Resolve any active dunning run — manual collect payment bypasses dunning retry
	if h.dunning != nil {
		if err := h.dunning.ResolveByInvoice(r.Context(), tenantID, id, domain.ResolutionPaymentRecovered); err != nil {
			slog.WarnContext(r.Context(), "failed to resolve dunning after collect payment", "invoice_id", id, "error", err)
		}
	}

	respond.JSON(w, r, http.StatusOK, charged)
}

// refund issues a direct refund on a paid invoice. Convenience wrapper around
// creditnote.Service.CreateRefund — the caller passes a reason + optional
// amount and gets back the issued credit note (which carries the Stripe
// refund ID and status). For partial refunds, amount_cents < amount_paid;
// default (amount_cents=0) refunds the full remaining refundable balance.
func (h *Handler) refund(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	if h.refundIssuer == nil {
		respond.Validation(w, r, "refund provider not configured")
		return
	}

	var body struct {
		AmountCents int64  `json:"amount_cents"`
		Reason      string `json:"reason"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}

	if body.AmountCents < 0 {
		respond.Validation(w, r, "amount_cents must be non-negative")
		return
	}
	if body.Reason == "" {
		respond.Validation(w, r, "reason is required")
		return
	}
	if !validRefundReasons[body.Reason] {
		respond.Validation(w, r, "reason must be one of: duplicate, fraudulent, requested_by_customer, other")
		return
	}

	cn, err := h.refundIssuer.IssueRefund(r.Context(), tenantID, RefundInput{
		InvoiceID:   id,
		AmountCents: body.AmountCents,
		Reason:      body.Reason,
		Description: body.Description,
	})
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "invoice")
		return
	}
	if err != nil {
		respond.FromError(w, r, err, "invoice")
		return
	}

	if h.auditLogger != nil {
		_ = h.auditLogger.Log(r.Context(), tenantID, domain.AuditActionRefund, "invoice", id, map[string]any{
			"invoice_id":          id,
			"credit_note_id":      cn.ID,
			"credit_note_number":  cn.CreditNoteNumber,
			"refund_amount_cents": cn.RefundAmountCents,
			"stripe_refund_id":    cn.StripeRefundID,
			"refund_status":       string(cn.RefundStatus),
			"reason":              cn.Reason,
			"currency":            cn.Currency,
		})
	}

	respond.JSON(w, r, http.StatusOK, cn)
}

// applyCoupon applies a coupon code to a draft invoice. Stripe-style
// flow: operator selects a coupon in the dashboard on an already-issued
// (but unfinalized) invoice; Velox redeems the coupon, recomputes tax
// against the post-discount base, and persists the snapshot atomically.
// Accepts Idempotency-Key for safe retries — a repeat with the same key
// returns the prior result with Idempotent-Replay: true.
func (h *Handler) applyCoupon(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	var body struct {
		Code           string `json:"code"`
		IdempotencyKey string `json:"idempotency_key,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}

	// Header wins over body so CLI/API clients can set the key the standard
	// way while the dashboard keeps a single body-only request shape (its
	// apiRequest helper doesn't support custom headers). Matches the
	// /customers/{id}/coupon pattern so two adjacent coupon endpoints don't
	// diverge on request conventions.
	idemKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idemKey == "" {
		idemKey = strings.TrimSpace(body.IdempotencyKey)
	}
	inv, err := h.svc.ApplyCoupon(r.Context(), tenantID, id, body.Code, idemKey)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "invoice")
		return
	}
	if err != nil {
		respond.FromError(w, r, err, "invoice")
		return
	}

	if h.auditLogger != nil {
		_ = h.auditLogger.Log(r.Context(), tenantID, domain.AuditActionApplyCoupon, "invoice", inv.ID, map[string]any{
			"invoice_number":     inv.InvoiceNumber,
			"customer_id":        inv.CustomerID,
			"coupon_code":        body.Code,
			"discount_cents":     inv.DiscountCents,
			"total_amount_cents": inv.TotalAmountCents,
			"currency":           inv.Currency,
		})
	}

	h.fireEvent(r.Context(), tenantID, domain.EventInvoiceCouponApplied, inv)

	respond.JSON(w, r, http.StatusOK, inv)
}

// retryTax re-runs tax calculation against a draft invoice in
// tax_status pending or failed. Backs the "Retry tax" action surfaced
// by the unified Attention shape. Idempotent — each call increments
// tax_retry_count and rewrites the per-line + invoice-level tax fields.
//
// 200 with the updated invoice (carrying the new Attention) on
// success or post-retry failure (a "still failing" retry is not an
// HTTP error — the dashboard wants the new code to render). 409 when
// the gate fails (status != draft, or tax_status not retryable). 404
// when the invoice doesn't exist.
func (h *Handler) retryTax(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	// Snapshot the pre-retry attention reason for the audit trail so
	// post-mortems can answer "did the retry change anything?".
	before, _ := h.svc.Get(r.Context(), tenantID, id)

	inv, err := h.svc.RetryTax(r.Context(), tenantID, id)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "invoice")
		return
	}
	if err != nil {
		respond.FromError(w, r, err, "invoice")
		return
	}

	if h.auditLogger != nil {
		var beforeReason, afterReason string
		if before.Attention != nil {
			beforeReason = string(before.Attention.Reason)
		}
		if inv.Attention != nil {
			afterReason = string(inv.Attention.Reason)
		}
		_ = h.auditLogger.Log(r.Context(), tenantID, domain.AuditActionRetryTax, "invoice", inv.ID, map[string]any{
			"invoice_number":    inv.InvoiceNumber,
			"customer_id":       inv.CustomerID,
			"tax_status":        inv.TaxStatus,
			"tax_retry_count":   inv.TaxRetryCount,
			"before_attention":  beforeReason,
			"after_attention":   afterReason,
			"tax_error_code":    inv.TaxErrorCode,
		})
	}

	respond.JSON(w, r, http.StatusOK, inv)
}

type timelineEvent struct {
	Timestamp       string `json:"timestamp"`
	Source          string `json:"source"` // "stripe" / "dunning" / "lifecycle" / "email"
	EventType       string `json:"event_type"`
	Status          string `json:"status"`
	Description     string `json:"description"`
	Error           string `json:"error,omitempty"`
	AmountCents     *int64 `json:"amount_cents,omitempty"`
	Currency        string `json:"currency,omitempty"`
	PaymentIntentID string `json:"payment_intent_id,omitempty"`
	AttemptCount    int    `json:"attempt_count,omitempty"`
	// Detail is a sub-line rendered beneath the row's main
	// description. Used today on invoice.paid for the payment
	// instrument ("via Visa •••• 4242"); generic enough that
	// future event types can attach their own context (e.g.
	// "after 3 retry attempts" on the same row in the dunning-
	// recovered case). Empty = no sub-line. ADR-020.
	Detail string `json:"detail,omitempty"`
	// IsSimulated marks events whose timestamp is in the simulated-
	// time domain (the owning sub is pinned to a test clock and this
	// event was produced by an engine-driven path — lifecycle, dunning).
	// Wall-clock-sourced events (stripe webhooks, email dispatcher,
	// operator audit actions) stay false even on a clock-pinned sub
	// because their timestamps reflect when they were actually
	// processed, not the simulated cycle they belong to.
	// SPA reads this flag directly — no client-side heuristic.
	IsSimulated bool `json:"is_simulated,omitempty"`
}

// withinWindow reports whether |a - b| <= window. Used by the
// timeline dedup to detect "this Stripe webhook co-occurred with
// the lifecycle column flip" without treating long-separated
// independent events as the same fact. Symmetric — order of args
// doesn't matter.
func withinWindow(a, b time.Time, window time.Duration) bool {
	d := a.Sub(b)
	if d < 0 {
		d = -d
	}
	return d <= window
}

// formatPaymentCardDetail produces the sub-line shown under the
// "Invoice paid" row, e.g. "via Visa •••• 4242". Returns empty
// when card details aren't on the invoice — graceful: no
// sub-line. Brand titlecased per Stripe convention
// (visa→Visa, mastercard→Mastercard). ADR-020.
func formatPaymentCardDetail(brand, last4 string) string {
	if last4 == "" && brand == "" {
		return ""
	}
	display := brandDisplayName(brand)
	if display == "" {
		display = "card"
	}
	if last4 == "" {
		return "via " + display
	}
	return "via " + display + " •••• " + last4
}

// brandDisplayName converts Stripe's enum-form card brand to the
// title-cased form operators read on the dashboard. Mirrors
// Stripe's own display names so the timeline matches what
// operators see in the Stripe dashboard.
//
// Stripe's PaymentMethodCard.DisplayBrand returns one of: visa,
// mastercard, american_express, cartes_bancaires, diners_club,
// discover, eftpos_australia, interac, jcb, union_pay, other —
// "and may contain more values in the future" per the SDK
// comment. Unknown values fall through to a defensive
// title-case so a future-Stripe addition doesn't render as
// "cartes_bancaires" in the dashboard.
func brandDisplayName(brand string) string {
	switch strings.ToLower(brand) {
	case "visa":
		return "Visa"
	case "mastercard":
		return "Mastercard"
	case "amex", "american_express", "american express":
		return "American Express"
	case "discover":
		return "Discover"
	case "jcb":
		return "JCB"
	case "diners", "diners_club":
		return "Diners Club"
	case "unionpay", "union_pay":
		return "UnionPay"
	case "cartes_bancaires":
		return "Cartes Bancaires"
	case "eftpos_australia":
		return "Eftpos Australia"
	case "interac":
		return "Interac"
	case "other":
		return "Card"
	case "":
		return ""
	default:
		// Unknown brand — title-case each underscore-separated
		// segment so a future-Stripe value renders legibly without
		// requiring a Velox release.
		return titleCaseSnake(brand)
	}
}

// titleCaseSnake turns "cartes_bancaires" into "Cartes Bancaires"
// for unrecognised brands. Defensive default so new Stripe
// networks render passably.
func titleCaseSnake(s string) string {
	parts := strings.Split(s, "_")
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + strings.ToLower(p[1:])
	}
	return strings.Join(parts, " ")
}

// relevantStripeEvents filters to only operator-meaningful events.
var relevantStripeEvents = map[string]bool{
	"payment_intent.succeeded":      true,
	"payment_intent.payment_failed": true,
	"payment_intent.canceled":       true,
}

func describeStripeEvent(eventType, failureMessage string) (string, string) {
	switch eventType {
	case "payment_intent.succeeded":
		return "Payment succeeded", "succeeded"
	case "payment_intent.payment_failed":
		return "Payment failed", "failed"
	case "payment_intent.canceled":
		return "Payment canceled", "canceled"
	default:
		return eventType, "info"
	}
}

// relevantDunningEvents filters to only operator-meaningful events.
var relevantDunningEvents = map[string]bool{
	"dunning_started": true,
	"retry_attempted": true,
	"resolved":        true,
	"escalated":       true,
}

// describeEmailEvent maps an email_outbox row to a timeline-friendly
// description + status. Returns empty description for email types
// that don't belong on the invoice timeline (catch-all so adding new
// templates doesn't accidentally surface them). Status maps to the
// existing dot-color grammar: succeeded (emerald), processing (blue),
// failed (red).
func describeEmailEvent(emailType, outboxStatus, _ string) (string, string) {
	desc := ""
	switch emailType {
	case "invoice":
		desc = "Invoice emailed to customer"
	case "payment_receipt":
		desc = "Payment receipt emailed"
	case "payment_failed":
		desc = "Payment-failed email sent to customer"
	case "payment_setup_request":
		desc = "Customer notified — set up payment method"
	case "dunning_warning":
		desc = "Dunning reminder emailed"
	case "dunning_escalation":
		desc = "Dunning escalation emailed"
	default:
		return "", ""
	}
	// Map outbox row status to timeline-status grammar.
	switch outboxStatus {
	case "dispatched":
		return desc, "succeeded"
	case "failed":
		return desc + " (delivery failed)", "failed"
	case "pending":
		return desc + " (queued)", "processing"
	}
	return desc, "succeeded"
}

func describeDunningEvent(eventType, reason string, attemptCount int) (string, string) {
	switch eventType {
	case "dunning_started":
		return "Automatic retry scheduled", "scheduled"
	case "retry_attempted":
		return fmt.Sprintf("Payment retry #%d attempted", attemptCount), "processing"
	case "resolved":
		switch reason {
		case "payment_recovered":
			return "Payment recovered via retry", "succeeded"
		case "manually_resolved":
			return "Resolved by operator", "resolved"
		default:
			return "Dunning resolved", "resolved"
		}
	case "escalated":
		switch reason {
		case "pause":
			return "Subscription paused — retries exhausted", "escalated"
		case "write_off_later":
			return "Marked for write-off — retries exhausted", "escalated"
		default:
			return "Escalated for manual review", "escalated"
		}
	default:
		return eventType, "info"
	}
}

func (h *Handler) paymentTimeline(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	inv, err := h.svc.Get(r.Context(), tenantID, id)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "invoice")
		return
	}
	if err != nil {
		respond.InternalError(w, r)
		return
	}

	// Draft invoices have no payment activity
	if inv.Status == domain.InvoiceDraft {
		respond.JSON(w, r, http.StatusOK, map[string]any{"events": []timelineEvent{}})
		return
	}

	// Resolve simulated-time context: if the owning sub is pinned to
	// a test clock, lifecycle + dunning events on this invoice were
	// produced in frozen-time and ship `is_simulated=true`. Stripe
	// webhook + email + audit events stay wall-clock either way.
	// Lookup failure is non-fatal — we just default to false (the SPA
	// degrades to no chip, which is correct on every non-clock-pinned
	// invoice anyway).
	subOnClock := false
	if h.subs != nil && inv.SubscriptionID != "" {
		if sub, err := h.subs.Get(r.Context(), tenantID, inv.SubscriptionID); err == nil {
			subOnClock = sub.TestClockID != ""
		}
	}

	var events []timelineEvent

	// Lifecycle events synthesised from invoice columns. Without these,
	// freshly-finalized invoices that haven't seen a Stripe charge yet
	// render an empty timeline — operators have no chronology to read.
	// Mirrors Stripe's "Invoice activity" section which always anchors
	// on Created → Finalized regardless of payment progress.
	events = append(events, timelineEvent{
		Timestamp:   inv.CreatedAt.Format(time.RFC3339),
		Source:      "lifecycle",
		EventType:   "invoice.created",
		Status:      "succeeded",
		Description: "Invoice created",
		IsSimulated: subOnClock,
	})
	if inv.IssuedAt != nil {
		amt := inv.AmountDueCents
		events = append(events, timelineEvent{
			Timestamp:   inv.IssuedAt.Format(time.RFC3339),
			Source:      "lifecycle",
			EventType:   "invoice.finalized",
			Status:      "succeeded",
			Description: "Invoice finalized",
			AmountCents: &amt,
			Currency:    inv.Currency,
			IsSimulated: subOnClock,
		})
	}
	// (Removed: synthetic "Payment deadline" event keyed off due_at.
	// Activity is for things that happened — charges, state
	// transitions, dunning attempts. A future deadline isn't an
	// activity. The deadline is already surfaced honestly in the
	// invoice header and the InvoiceAttention banner's `DueBy` line.)
	if inv.VoidedAt != nil {
		events = append(events, timelineEvent{
			Timestamp:   inv.VoidedAt.Format(time.RFC3339),
			Source:      "lifecycle",
			EventType:   "invoice.voided",
			Status:      "canceled",
			Description: "Invoice voided",
			IsSimulated: subOnClock,
		})
	}
	if inv.PaidAt != nil {
		amt := inv.AmountPaidCents
		events = append(events, timelineEvent{
			Timestamp:   inv.PaidAt.Format(time.RFC3339),
			Source:      "lifecycle",
			EventType:   "invoice.paid",
			Status:      "succeeded",
			Description: "Invoice paid",
			AmountCents: &amt,
			Currency:    inv.Currency,
			Detail:      formatPaymentCardDetail(inv.PaymentCardBrand, inv.PaymentCardLast4),
			IsSimulated: subOnClock,
		})
	}

	// Customer-notification email events. Surfaces "Customer notified
	// — payment method required" / "Receipt sent" / "Dunning warning
	// emailed" alongside the Stripe + dunning rows. Without this,
	// operators have no signal that the customer was actually told
	// about the issue — the email outbox is the durable trace.
	if h.emailEvents != nil {
		emailEvts, err := h.emailEvents.ListByInvoice(r.Context(), tenantID, inv.InvoiceNumber)
		if err == nil {
			for _, evt := range emailEvts {
				desc, status := describeEmailEvent(evt.EmailType, evt.Status, evt.LastError)
				if desc == "" {
					continue
				}
				// Use dispatched_at when the row was actually delivered
				// so the timeline reflects send-time, not enqueue-time.
				ts := evt.CreatedAt
				if evt.DispatchedAt != nil {
					ts = *evt.DispatchedAt
				}
				events = append(events, timelineEvent{
					Timestamp:   ts.Format(time.RFC3339),
					Source:      "email",
					EventType:   "email." + evt.EmailType,
					Status:      status,
					Description: desc,
					Error:       evt.LastError,
				})
			}
		}
	}

	// Fetch Stripe webhook events — only operator-relevant ones.
	//
	// Dedup vs lifecycle (ADR-020): the Stripe webhook IS what
	// triggers the lifecycle column flip, so the rows describe one
	// fact from two angles. Drop the Stripe row when its lifecycle
	// counterpart is set:
	//   payment_intent.succeeded → drop when PaidAt != nil
	//   payment_intent.canceled  → drop when VoidedAt != nil (a PI
	//     can also cancel without a void — e.g. expired PI — so this
	//     is conditional, not unconditional)
	// payment_intent.payment_failed has no lifecycle counterpart;
	// always keep — it's the only signal of a failed charge attempt.
	if h.webhookEvents != nil {
		webhookEvts, err := h.webhookEvents.ListByInvoice(r.Context(), tenantID, id)
		if err == nil {
			for _, evt := range webhookEvts {
				if !relevantStripeEvents[evt.EventType] {
					continue
				}
				switch evt.EventType {
				case "payment_intent.succeeded":
					// PI succeeded ALWAYS sets PaidAt synchronously
					// in the same handler — they co-occur within
					// milliseconds. Unconditional drop is correct.
					if inv.PaidAt != nil {
						continue
					}
				case "payment_intent.canceled":
					// PI cancel CAN co-occur with a void (operator
					// voids a finalized invoice with a pending PI)
					// but can also fire independently (PI 24h-expiry
					// with no void; void of a draft with no PI).
					// Use a timestamp window to dedup only the
					// co-occurrence case — an unconditional drop on
					// "VoidedAt is non-nil" over-suppresses when a
					// PI cancels long before an unrelated later
					// void. 5min covers wall-clock drift between
					// Stripe's event time and our void time but
					// doesn't bleed into separate operator events.
					if inv.VoidedAt != nil &&
						withinWindow(*inv.VoidedAt, evt.OccurredAt, 5*time.Minute) {
						continue
					}
				}
				desc, status := describeStripeEvent(evt.EventType, evt.FailureMessage)
				events = append(events, timelineEvent{
					Timestamp:       evt.OccurredAt.Format(time.RFC3339),
					Source:          "stripe",
					EventType:       evt.EventType,
					Status:          status,
					Description:     desc,
					Error:           evt.FailureMessage,
					AmountCents:     evt.AmountCents,
					Currency:        evt.Currency,
					PaymentIntentID: evt.PaymentIntentID,
				})
			}
		}
	}

	// Fetch dunning events. Track the max attempt count across the
	// run so we can attach it to the lifecycle invoice.paid row
	// when this run resolved into payment success — the operator
	// then sees "Invoice paid · after 3 retry attempts" in one row
	// instead of separate paid + dunning-resolved entries.
	//
	// Also collect retry-attempted timestamps so we can dedup against
	// Stripe `payment_intent.payment_failed` events below: each
	// failed dunning retry produces BOTH a Stripe webhook (raw
	// "Payment failed") and a dunning event (operator-meaningful
	// "Retry attempted N of M"). The Stripe row is the redundant one
	// (dunning row carries attempt context); drop it when a dunning
	// retry-attempted sits within a 5-minute window of the Stripe
	// event. Initial payment failures — before any retries — have no
	// retry-attempted counterpart and stay un-deduped.
	maxAttemptCount := 0
	var retryAttemptedTimes []time.Time
	if h.dunningTimeline != nil {
		runs, err := h.dunningTimeline.ListRunsByInvoice(r.Context(), tenantID, id)
		if err == nil {
			for _, run := range runs {
				runEvents, err := h.dunningTimeline.ListEvents(r.Context(), tenantID, run.ID)
				if err != nil {
					continue
				}
				for _, evt := range runEvents {
					if !relevantDunningEvents[string(evt.EventType)] {
						continue
					}
					if evt.AttemptCount > maxAttemptCount {
						maxAttemptCount = evt.AttemptCount
					}
					if string(evt.EventType) == "retry_attempted" {
						retryAttemptedTimes = append(retryAttemptedTimes, evt.CreatedAt)
					}
					// Suppress dunning 'resolved' when the lifecycle
					// invoice.paid row will already say it. Distinct
					// resolution paths (manually_resolved without
					// payment) keep the dunning row — only the
					// payment-recovered case is redundant with paid.
					if string(evt.EventType) == "resolved" &&
						evt.Reason == "payment_recovered" &&
						inv.PaidAt != nil {
						continue
					}
					desc, status := describeDunningEvent(string(evt.EventType), evt.Reason, evt.AttemptCount)
					events = append(events, timelineEvent{
						Timestamp:    evt.CreatedAt.Format(time.RFC3339),
						Source:       "dunning",
						EventType:    string(evt.EventType),
						Status:       status,
						Description:  desc,
						Error:        evt.Reason,
						AttemptCount: evt.AttemptCount,
						IsSimulated:  subOnClock,
					})
				}
			}
		}
	}

	// Dedup Stripe payment_intent.payment_failed against the dunning
	// retry_attempted rows collected above. Walk `events` and drop
	// any Stripe payment_failed whose timestamp falls within 5 min of
	// a retry_attempted event. 5 min covers clock skew between the
	// Stripe webhook arrival and the dunning event write without
	// bleeding into unrelated failures (retries are spaced days apart).
	if len(retryAttemptedTimes) > 0 {
		filtered := events[:0]
		for _, evt := range events {
			if evt.Source == "stripe" && evt.EventType == "payment_intent.payment_failed" {
				ts, parseErr := time.Parse(time.RFC3339, evt.Timestamp)
				if parseErr == nil {
					coincident := false
					for _, ra := range retryAttemptedTimes {
						if withinWindow(ts, ra, 5*time.Minute) {
							coincident = true
							break
						}
					}
					if coincident {
						continue
					}
				}
			}
			filtered = append(filtered, evt)
		}
		events = filtered
	}

	// Attach attempt count to the lifecycle invoice.paid row when
	// the invoice was collected via dunning retry. The frontend
	// renders "after N retry attempts" as a sub-line, replacing
	// the now-suppressed dunning 'resolved' row.
	if inv.PaidAt != nil && maxAttemptCount > 0 {
		for i := range events {
			if events[i].Source == "lifecycle" && events[i].EventType == "invoice.paid" {
				events[i].AttemptCount = maxAttemptCount
				break
			}
		}
	}

	// Sort by timestamp ascending
	sort.Slice(events, func(i, j int) bool {
		return events[i].Timestamp < events[j].Timestamp
	})

	respond.JSON(w, r, http.StatusOK, map[string]any{"events": events})
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

	// Build Bill To from customer + billing profile
	bt := BillToInfo{Name: inv.CustomerID}
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
				BrandColor:   ts.BrandColor,
				TaxID:        ts.TaxID,
				TaxIDType:    SupplierTaxIDTypeFromCountry(ts.CompanyCountry),
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
	}

	pdfBytes, err := RenderPDF(r.Context(), inv, items, bt, cnInfos, ci)
	if err != nil {
		respond.InternalError(w, r)
		return
	}

	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", "inline; filename=\""+inv.InvoiceNumber+".pdf\"")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pdfBytes)
}
