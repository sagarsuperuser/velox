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

	"github.com/sagarsuperuser/velox/internal/api/middleware"
	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/api/timefilter"
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
	ChargeInvoice(ctx context.Context, tenantID string, inv domain.Invoice, stripeCustomerID, stripePaymentMethodID string) (domain.Invoice, error)
}

// PaymentSetupGetter checks if a customer has a payment method ready.
type PaymentSetupGetter interface {
	GetPaymentSetup(ctx context.Context, tenantID, customerID string) (domain.CustomerPaymentSetup, error)
}

// NoPaymentMethodNotifier emails the customer a payment-update link when a
// finalized invoice can't be auto-charged because no payment method is on
// file. Structurally identical to the billing engine's notifier of the same
// name (wired to the same adapter in router.go) — declared locally so the
// invoice package doesn't import the billing engine (zero cross-domain
// imports). Optional; nil means no-PM finalize just queues for retry.
type NoPaymentMethodNotifier interface {
	NotifyNoPaymentMethod(ctx context.Context, tenantID string, inv domain.Invoice) error
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
	Status       string // pending / dispatched / failed
	CreatedAt    time.Time
	DispatchedAt *time.Time
	LastError    string
	To           string // resolved from payload
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

type Handler struct {
	svc             *Service
	customers       CustomerGetter
	settings        SettingsGetter
	creditNotes     CreditNoteLister
	charger         PaymentCharger
	paymentSetups   PaymentSetupGetter
	paymentCancel   PaymentCanceler
	dunning         DunningResolver
	webhookEvents   WebhookEventLister
	emailEvents     EmailEventLister
	dunningTimeline DunningTimelineFetcher
	events          domain.EventDispatcher
	emailSender     EmailSender
	refundIssuer    RefundIssuer
	auditLogger     auditWriter
	noPMNotifier    NoPaymentMethodNotifier
}

// auditWriter is the narrow audit-write interface the invoice handler uses.
// *audit.Logger satisfies it; declared as an interface (vs the concrete
// logger) so the handler's audit rows — action, label, metadata — are
// unit-testable with a capturing fake.
type auditWriter interface {
	Log(ctx context.Context, tenantID, action, resourceType, resourceID, resourceLabel string, metadata map[string]any) error
}

type HandlerDeps struct {
	CreditNotes     CreditNoteLister
	Charger         PaymentCharger
	PaymentSetups   PaymentSetupGetter
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

// SetNoPaymentMethodNotifier wires the customer-notification dispatcher
// used when a manually-finalized invoice can't be auto-charged (no PM on
// file). Mirrors the billing engine's wiring — both receive the same
// adapter instance — so a manual one-off invoice and a cycle invoice notify
// the customer identically at finalize. Optional; nil → no-PM finalize
// still queues for scheduler retry, just without the email.
func (h *Handler) SetNoPaymentMethodNotifier(n NoPaymentMethodNotifier) {
	h.noPMNotifier = n
}

// SetEmailEvents wires the email_outbox lister used by the timeline
// to surface customer-notification events. Optional — when nil, the
// timeline omits the email rows but the rest of it (lifecycle,
// stripe webhooks, dunning) renders unchanged.
func (h *Handler) SetEmailEvents(lister EmailEventLister) {
	h.emailEvents = lister
}

// SetAuditLogger configures audit logging for financial operations.
func (h *Handler) SetAuditLogger(l auditWriter) { h.auditLogger = l }

// fireEvent dispatches a webhook event. Synchronous: with the outbox
// (RES-1) Dispatch is a short DB insert that must persist-before-return,
// and logging any failure is preferred to silently losing the event.
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
	r.Post("/{id}/resend-setup-link", h.resendSetupLink)
	r.Post("/{id}/collect", h.collectPayment)
	r.Post("/{id}/refund", h.refund)
	r.Post("/{id}/retry-tax", h.retryTax)
	r.Post("/{id}/rotate-public-token", h.rotatePublicToken)
	// Stripe-parity offline-payment recovery. Lets the operator mark
	// an unpaid (or uncollectible) invoice as paid without going
	// through a PaymentIntent — for cheque, wire, cash, or any
	// out-of-band collection. Stripe's equivalent is the
	// paid_out_of_band=true flag on POST /v1/invoices/{id}/pay.
	r.Post("/{id}/record-payment", h.recordOfflinePayment)
	// Stripe-parity uncollectible mark — operator-driven path. The
	// dunning automation reaches this same service method via the
	// mark_uncollectible final-action; this endpoint lets the
	// operator do it directly without waiting for retries.
	r.Post("/{id}/mark-uncollectible", h.markUncollectible)
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

	// Shared ?from / ?to contract (api/timefilter): RFC3339 instants
	// or bare YYYY-MM-DD, inclusive both ends. Malformed input is a
	// loud 400 — silently ignoring it would return an unfiltered list
	// that lies about what the operator asked for.
	createdFrom, createdTo, err := timefilter.ParseRange(r, "from", "to")
	if err != nil {
		respond.FromError(w, r, err, "invoice")
		return
	}

	filter := ListFilter{
		TenantID:       tenantID,
		CustomerID:     r.URL.Query().Get("customer_id"),
		SubscriptionID: r.URL.Query().Get("subscription_id"),
		Status:         r.URL.Query().Get("status"),
		PaymentStatus:  r.URL.Query().Get("payment_status"),
		Search:         strings.TrimSpace(r.URL.Query().Get("search")),
		CreatedFrom:    createdFrom,
		CreatedTo:      createdTo,
		Overdue:        r.URL.Query().Get("overdue") == "true",
		IDs:            ids,
		Limit:          limit,
		Offset:         offset,
		// Sort + direction are validated against a closed set in
		// the store (invoiceOrderBy). Unknown sort keys silently
		// default to created_at; unknown dir defaults to desc.
		Sort:    r.URL.Query().Get("sort"),
		SortDir: r.URL.Query().Get("dir"),
	}
	// Cursor pagination (2026-05-29). Takes precedence over offset.
	// Only supported on the default sort (created_at DESC) — a custom
	// sort + cursor combination would yield inconsistent seek-vs-
	// order pairings.
	if c := r.URL.Query().Get("after"); c != "" && filter.Sort == "" {
		if cur, err := middleware.DecodeCursor(c); err == nil {
			filter.AfterCreatedAt = cur.CreatedAt
			filter.AfterID = cur.ID
		}
	}

	invoices, total, err := h.svc.List(r.Context(), filter)
	if err != nil {
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(), "list invoices", "error", err)
		return
	}
	if invoices == nil {
		invoices = []domain.Invoice{}
	}

	if !filter.AfterCreatedAt.IsZero() && filter.AfterID != "" {
		l := filter.Limit
		if l <= 0 {
			l = 50
		} else if l > 100 {
			l = 100
		}
		hasMore := len(invoices) > l
		if hasMore {
			invoices = invoices[:l]
		}
		resp := middleware.PageResponse{Data: invoices, HasMore: hasMore}
		if hasMore && len(invoices) > 0 {
			last := invoices[len(invoices)-1]
			resp.NextCursor = middleware.EncodeCursor(last.ID, last.CreatedAt)
		}
		respond.JSON(w, r, http.StatusOK, resp)
		return
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

	// The finalize audit row AND the invoice.finalized webhook are emitted
	// by service.Finalize — the canonical single writer, covering this
	// endpoint AND the tax-retry auto-finalize. Pre-fix the webhook fired
	// from here only, so the tax-retry path silently skipped it.

	// No automatic "here's your invoice" email on finalize. Velox
	// auto-charges the saved card (Stripe charge_automatically model), so
	// the customer's touchpoint is the payment receipt on success (fired
	// from the Stripe webhook) or the "set up payment method" email below
	// when there's no card on file — matching cycle invoices and Stripe's
	// auto-charge behavior, where finalizing an auto-charged invoice does
	// NOT email the invoice. Operators can still send it explicitly via
	// POST /{id}/send.

	inv = h.collectAtFinalize(r.Context(), tenantID, inv)

	respond.JSON(w, r, http.StatusOK, inv)
}

// collectAtFinalize runs the post-finalize collection step and returns the
// possibly-updated invoice. It mirrors the billing engine's cycle-invoice
// post-finalize block so a manual one-off invoice collects identically:
//   - payment method ready → auto-charge the saved card (the Stripe webhook
//     fires the receipt on success; a decline starts dunning).
//   - no payment method → queue for the scheduler's auto-charge retry (which
//     charges the moment the customer attaches a card) AND email the customer
//     a payment-update link. Pre-fix the no-PM case did nothing, so a manual
//     invoice silently went overdue — customer never told, scheduler never
//     retried.
func (h *Handler) collectAtFinalize(ctx context.Context, tenantID string, inv domain.Invoice) domain.Invoice {
	if h.charger == nil || h.paymentSetups == nil || inv.AmountDueCents <= 0 {
		return inv
	}
	ps, psErr := h.paymentSetups.GetPaymentSetup(ctx, tenantID, inv.CustomerID)
	pmReady := psErr == nil && ps.SetupStatus == domain.PaymentSetupReady && ps.StripeCustomerID != ""
	if pmReady {
		if charged, err := h.charger.ChargeInvoice(ctx, tenantID, inv, ps.StripeCustomerID, ps.StripePaymentMethodID); err != nil {
			// A failed charge attempt starts dunning (the single retry
			// owner), so we deliberately do NOT also set auto_charge_pending
			// here — the scheduler clears that flag for declines anyway and
			// defers to dunning; setting it would be redundant.
			slog.WarnContext(ctx, "auto-charge failed, invoice stays finalized; dunning drives collection",
				"invoice_id", inv.ID, "error", err)
		} else {
			inv = charged
			slog.InfoContext(ctx, "auto-charge initiated", "invoice_id", inv.ID)
		}
		return inv
	}
	// No payment method on file: no charge is attempted, so dunning never
	// starts — the scheduler flag is the only retry path.
	slog.InfoContext(ctx, "no payment method at finalize, queuing for scheduler retry + notifying customer",
		"invoice_id", inv.ID, "customer_id", inv.CustomerID)
	if err := h.svc.SetAutoChargePending(ctx, tenantID, inv.ID, true); err != nil {
		slog.WarnContext(ctx, "failed to mark invoice for auto-charge retry",
			"invoice_id", inv.ID, "error", err)
	}
	if h.noPMNotifier != nil {
		if err := h.noPMNotifier.NotifyNoPaymentMethod(ctx, tenantID, inv); err != nil {
			slog.WarnContext(ctx, "no-payment-method notification failed",
				"invoice_id", inv.ID, "error", err)
		}
	}
	return inv
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

	// Consumed-credit reversal now happens atomically inside svc.Void (status
	// flip + reversal in one tx) — single-writer, no separate best-effort step.

	// Resolve any active dunning runs for this invoice
	if h.dunning != nil {
		if err := h.dunning.ResolveByInvoice(r.Context(), tenantID, id, domain.ResolutionManuallyResolved); err != nil {
			slog.WarnContext(r.Context(), "failed to resolve dunning on void", "invoice_id", id, "error", err)
		} else {
			slog.InfoContext(r.Context(), "dunning resolved on invoice void", "invoice_id", id)
		}
	}

	if h.auditLogger != nil {
		_ = h.auditLogger.Log(r.Context(), tenantID, domain.AuditActionVoid, "invoice", inv.ID, inv.InvoiceNumber, map[string]any{
			"invoice_number":     inv.InvoiceNumber,
			"customer_id":        inv.CustomerID,
			"total_amount_cents": inv.TotalAmountCents,
			"currency":           inv.Currency,
		})
	}

	// invoice.voided is emitted by service.Void (single-writer — covers
	// this endpoint AND engine-triggered voids via InvoiceVoider).

	respond.JSON(w, r, http.StatusOK, inv)
}

// resendSetupLink re-emails the customer the payment-METHOD setup link for a
// finalized, unpaid invoice with no card on file — the "Resend setup link"
// nudge on the no_payment_method attention card. It re-sends the SAME email the
// engine auto-sent at finalize (NotifyNoPaymentMethod → Stripe Checkout in
// SETUP mode → engine auto-charges once a card is attached), which matches that
// state's auto-charge-on-attach model. This is deliberately distinct from
// POST /{id}/send, which emails the hosted-invoice "pay this invoice" link
// (Checkout in PAYMENT mode) — a different collection path for states where the
// operator wants the customer to pay directly.
func (h *Handler) resendSetupLink(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	inv, err := h.svc.Get(r.Context(), tenantID, id)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "invoice")
		return
	}
	if err != nil {
		respond.FromError(w, r, err, "invoice")
		return
	}
	// Only meaningful while collection is still pending: a finalized invoice
	// that hasn't been paid. Draft/voided/paid invoices have no setup link to
	// resend.
	if inv.Status != domain.InvoiceFinalized || inv.PaymentStatus == domain.PaymentSucceeded {
		respond.Error(w, r, http.StatusConflict, "invalid_state", "invoice_not_collectible",
			"setup link can only be resent for a finalized, unpaid invoice")
		return
	}
	if h.noPMNotifier == nil {
		slog.ErrorContext(r.Context(), "resend setup link: no-PM notifier not wired", "invoice_id", inv.ID)
		respond.InternalError(w, r)
		return
	}
	if err := h.noPMNotifier.NotifyNoPaymentMethod(r.Context(), tenantID, inv); err != nil {
		respond.FromError(w, r, err, "invoice")
		return
	}

	if h.auditLogger != nil {
		_ = h.auditLogger.Log(r.Context(), tenantID, domain.AuditActionSend, "invoice", inv.ID, inv.InvoiceNumber, map[string]any{
			"action":         "resend_setup_link",
			"invoice_number": inv.InvoiceNumber,
			"customer_id":    inv.CustomerID,
		})
	}

	respond.JSON(w, r, http.StatusOK, map[string]string{"status": "sent"})
}

// markUncollectible is the operator-driven Stripe-parity bad-debt
// write-off. Service-layer write + event already happen inside
// invoice.Service.MarkUncollectible (so the dunning automated path
// and ResolveRun(invoice_not_collectible) get the same contract);
// this handler adds the side-effects that only fire on the direct
// operator action: resolve any active dunning run so retry
// automation halts immediately.
//
// Industry parity: Stripe + Recurly both halt all dunning activity
// when an invoice is marked uncollectible / failed. We resolve the
// active dunning run with ResolutionRetriesExhausted-shape semantics
// (NOT invoice_not_collectible, which would loop back into the
// "also mark invoice uncollectible" branch we just executed).
func (h *Handler) markUncollectible(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	inv, err := h.svc.MarkUncollectible(r.Context(), tenantID, id)
	if err != nil {
		respond.FromError(w, r, err, "invoice")
		return
	}

	// The marked_uncollectible audit row is written by service.MarkUncollectible
	// (the canonical writer, with richer metadata). The handler-level write that
	// used to live here — added under the mistaken belief no row existed — made
	// every operator mark-uncollectible produce TWO identical audit rows.

	// Halt dunning automation. Best-effort — failure is logged not
	// surfaced; the invoice transition is the authoritative state
	// change, and dunning runs scan the invoice status on next tick
	// anyway. Using ResolutionManuallyResolved (not the
	// invoice_not_collectible resolution) because the invoice flip
	// already happened above; passing the matching resolution would
	// recurse via ResolveRun's cross-flow branch we just added.
	if h.dunning != nil {
		if err := h.dunning.ResolveByInvoice(r.Context(), tenantID, id, domain.ResolutionManuallyResolved); err != nil {
			slog.WarnContext(r.Context(), "failed to resolve dunning on mark-uncollectible", "invoice_id", id, "error", err)
		}
	}

	respond.JSON(w, r, http.StatusOK, inv)
}

// recordOfflinePayment flips an unpaid (or uncollectible) invoice to
// paid based on operator-recorded out-of-band collection (cheque,
// wire, cash). Stripe-parity: their paid_out_of_band=true flag on
// POST /v1/invoices/{id}/pay surfaces the same recovery path.
//
// Body shape: { "note": "Cheque #1234" } — single optional string.
// Amount is implicit (full amount_due); partial payments deferred to
// when a customer asks. Date stamps as clock.Now (sim-time on
// clock-pinned invoices).
//
// Side-effects: resolves any active dunning run with
// ResolutionPaymentRecovered so the dashboard reflects the recovery
// in the dunning history view.
func (h *Handler) recordOfflinePayment(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	var input struct {
		Note string `json:"note"`
	}
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			respond.BadRequest(w, r, "invalid JSON body")
			return
		}
	}

	inv, err := h.svc.RecordOfflinePayment(r.Context(), tenantID, id, input.Note)
	if err != nil {
		respond.FromError(w, r, err, "invoice")
		return
	}

	// The payment_recorded audit row is written by service.RecordOfflinePayment
	// (the canonical writer — its row also carries recovered_from_status). The
	// handler-level write that used to live here duplicated it on every call.

	if h.dunning != nil {
		if err := h.dunning.ResolveByInvoice(r.Context(), tenantID, id, domain.ResolutionPaymentRecovered); err != nil {
			slog.WarnContext(r.Context(), "failed to resolve dunning on record-payment", "invoice_id", id, "error", err)
		}
	}

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
		_ = h.auditLogger.Log(r.Context(), tenantID, domain.AuditActionRotate, "invoice", inv.ID, inv.InvoiceNumber, map[string]any{
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

	// One shared context builder across emailed/downloaded/hosted PDFs —
	// this path previously hand-rolled a THINNER context (no buyer
	// address/tax id, no credit notes), so the emailed document diverged
	// from the downloaded one.
	bt, ci, cnInfos := BuildPDFContext(r.Context(), h.customers, h.settings, h.creditNotes, tenantID, &inv)

	pdfBytes, err := RenderPDF(r.Context(), inv, items, bt, cnInfos, ci)
	if err != nil {
		respond.InternalError(w, r)
		return
	}

	// AmountDueCents, not Total: the email template labels this figure
	// "Amount due", and credits/partial payments make the two differ —
	// telling a customer they owe the pre-credit total is wrong.
	if err := h.emailSender.SendInvoice(r.Context(), tenantID, body.Email, bt.Name, inv.InvoiceNumber, inv.AmountDueCents, inv.Currency, pdfBytes, inv.PublicToken); err != nil {
		// Sanitize at the boundary — SMTP errors / outbox-store errors
		// would otherwise leak to the operator toast. ADR-026.
		respond.FromError(w, r, err, "invoice_email")
		return
	}

	// Explicit audit row so an operator-initiated send is recorded as
	// "Emailed INV-NNN", not the middleware catch-all's generic "create".
	// No recipient address in the append-only row (GDPR erasure) — the email
	// outbox is the delivery record; the row links the invoice + customer.
	if h.auditLogger != nil {
		_ = h.auditLogger.Log(r.Context(), tenantID, domain.AuditActionSend, "invoice", inv.ID, inv.InvoiceNumber, map[string]any{
			"invoice_number": inv.InvoiceNumber,
			"customer_id":    inv.CustomerID,
		})
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

	charged, err := h.charger.ChargeInvoice(r.Context(), tenantID, inv, ps.StripeCustomerID, ps.StripePaymentMethodID)
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

	// Explicit audit row for the money-movement action. Without it the
	// middleware catch-all records POST /collect as a generic "create"
	// ("Created INV-NNN"), indistinguishable from the invoice's creation.
	// MarkHandled (inside Log) suppresses that catch-all.
	if h.auditLogger != nil {
		_ = h.auditLogger.Log(r.Context(), tenantID, domain.AuditActionCollect, "invoice", charged.ID, charged.InvoiceNumber, map[string]any{
			"invoice_number": charged.InvoiceNumber,
			"amount_cents":   inv.AmountDueCents,
			"currency":       inv.Currency,
		})
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
		// Label the row with the invoice number so it reads "Refunded
		// INV-NNN" (a money-out action), matching finalize/void rows.
		refundLabel := ""
		if refInv, gErr := h.svc.Get(r.Context(), tenantID, id); gErr == nil {
			refundLabel = refInv.InvoiceNumber
		}
		_ = h.auditLogger.Log(r.Context(), tenantID, domain.AuditActionRefund, "invoice", id, refundLabel, map[string]any{
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
		_ = h.auditLogger.Log(r.Context(), tenantID, domain.AuditActionRetryTax, "invoice", inv.ID, inv.InvoiceNumber, map[string]any{
			"invoice_number":   inv.InvoiceNumber,
			"customer_id":      inv.CustomerID,
			"tax_status":       inv.TaxStatus,
			"tax_retry_count":  inv.TaxRetryCount,
			"before_attention": beforeReason,
			"after_attention":  afterReason,
			"tax_error_code":   inv.TaxErrorCode,
		})
	}

	respond.JSON(w, r, http.StatusOK, inv)
}

type timelineEvent struct {
	Timestamp       string `json:"timestamp"`
	Source          string `json:"source"` // "stripe" / "dunning" / "lifecycle" / "email" / "credit_note"
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

// dropCanceledForVoid reports whether a payment_intent.canceled webhook row
// should be suppressed from the timeline because it co-occurred with a void
// (the void cancels the invoice's pending PI — one fact, two angles).
//
// A void with no PI cancel, or a PI cancel with no void (24h expiry), must NOT
// suppress anything — hence the nil guard. For a wall-clock invoice we only
// drop within a 5-minute window so an unrelated *earlier* PI expiry isn't
// suppressed by a much-later void. For a clock-pinned (simulated) invoice the
// window can't apply — voidedAt is test-clock time while occurredAt (Stripe)
// is wall-clock, so they're years apart and withinWindow never matches; there
// a void unconditionally implies the pending PI was canceled, so drop.
func dropCanceledForVoid(voidedAt *time.Time, occurredAt time.Time, isSimulated bool) bool {
	if voidedAt == nil {
		return false
	}
	return isSimulated || withinWindow(*voidedAt, occurredAt, 5*time.Minute)
}

// foldEmailIntoStripeFailed collapses a successfully dispatched
// "Payment-failed email sent to customer" row into its co-occurring
// Stripe payment_intent.payment_failed row as a Detail sub-line.
// Both rows are wall-clock-stamped (email dispatcher and Stripe
// webhook both run in real time), so a tight window matches
// reliably even for test-clock-pinned invoices. Only succeeded
// sends fold — pending or failed deliveries stay as standalone
// rows so operators see delivery problems. One-to-one matching;
// excess rows in either direction survive.
func foldEmailIntoStripeFailed(events []timelineEvent, window time.Duration) []timelineEvent {
	type pair struct{ stripeIdx, emailIdx int }
	var pairs []pair
	claimedStripe := make(map[int]bool)
	for j := range events {
		e := events[j]
		if e.Source != "email" || e.EventType != "email.payment_failed" {
			continue
		}
		if e.Status != "succeeded" {
			continue
		}
		eTS, err := time.Parse(time.RFC3339, e.Timestamp)
		if err != nil {
			continue
		}
		for i := range events {
			if claimedStripe[i] {
				continue
			}
			s := events[i]
			if s.Source != "stripe" || s.EventType != "payment_intent.payment_failed" {
				continue
			}
			sTS, err := time.Parse(time.RFC3339, s.Timestamp)
			if err != nil {
				continue
			}
			if !withinWindow(sTS, eTS, window) {
				continue
			}
			pairs = append(pairs, pair{stripeIdx: i, emailIdx: j})
			claimedStripe[i] = true
			break
		}
	}
	if len(pairs) == 0 {
		return events
	}
	for _, p := range pairs {
		s := &events[p.stripeIdx]
		if s.Detail == "" {
			s.Detail = "Customer notified by email"
		}
	}
	dropIdx := make(map[int]bool, len(pairs))
	for _, p := range pairs {
		dropIdx[p.emailIdx] = true
	}
	out := make([]timelineEvent, 0, len(events)-len(pairs))
	for i, evt := range events {
		if dropIdx[i] {
			continue
		}
		out = append(out, evt)
	}
	return out
}

// mergeFailedPaymentTwins folds Stripe payment_intent.payment_failed
// rows into corresponding dunning [dunning_started, retry_attempted]
// rows by chronological index — the k-th Stripe failure on an
// invoice pairs with the k-th dunning attempt event. Pairing by
// index (not time window) is required because test-clock-pinned
// invoices emit dunning events in frozen time while Stripe webhooks
// land in wall-clock time; the two timestamps can differ by months,
// far outside any reasonable window. Within an invoice the
// dunning state machine produces attempt events in strict order
// (started → retry #1 → retry #2 → …) and each corresponds to
// exactly one Stripe charge attempt, so index pairing is canonical.
//
// The Stripe row's PaymentIntent id + amount + currency + error +
// Detail lift onto the dunning row (which carries operator-meaningful
// attempt # + scheduled-next context), then the Stripe row drops.
// One-to-one pairing; excess Stripe rows survive (rare — dunning
// disabled or lagging), excess dunning rows survive (rare — Stripe
// webhook hasn't arrived yet).
func mergeFailedPaymentTwins(events []timelineEvent) []timelineEvent {
	var stripeIdxs, dunningIdxs []int
	for i, e := range events {
		if e.Source == "stripe" && e.EventType == "payment_intent.payment_failed" {
			stripeIdxs = append(stripeIdxs, i)
			continue
		}
		if e.Source == "dunning" && (e.EventType == "dunning_started" || e.EventType == "retry_attempted") {
			dunningIdxs = append(dunningIdxs, i)
		}
	}
	sort.SliceStable(stripeIdxs, func(a, b int) bool {
		return events[stripeIdxs[a]].Timestamp < events[stripeIdxs[b]].Timestamp
	})
	sort.SliceStable(dunningIdxs, func(a, b int) bool {
		return events[dunningIdxs[a]].Timestamp < events[dunningIdxs[b]].Timestamp
	})
	pairs := min(len(stripeIdxs), len(dunningIdxs))
	if pairs == 0 {
		return events
	}
	dropIdx := make(map[int]bool, pairs)
	for k := range pairs {
		sIdx := stripeIdxs[k]
		dIdx := dunningIdxs[k]
		s := events[sIdx]
		d := &events[dIdx]
		if d.PaymentIntentID == "" {
			d.PaymentIntentID = s.PaymentIntentID
		}
		if d.AmountCents == nil {
			d.AmountCents = s.AmountCents
		}
		if d.Currency == "" {
			d.Currency = s.Currency
		}
		if d.Error == "" {
			d.Error = s.Error
		}
		if d.Detail == "" {
			d.Detail = s.Detail
		}
		dropIdx[sIdx] = true
	}
	out := make([]timelineEvent, 0, len(events)-len(dropIdx))
	for i, evt := range events {
		if dropIdx[i] {
			continue
		}
		out = append(out, evt)
	}
	return out
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
		// reason carries the policy.final_action that fired. ADR-036
		// amendment aligned the enum with Stripe/Lago/Recurly: pause
		// now means pause-collection (keep_as_draft), not hard pause;
		// write_off_later → mark_uncollectible; new cancel_subscription.
		switch reason {
		case "pause":
			return "Collection paused — retries exhausted", "escalated"
		case "mark_uncollectible":
			return "Marked uncollectible — retries exhausted", "escalated"
		case "cancel_subscription":
			return "Subscription canceled — retries exhausted", "escalated"
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

	// is_simulated is the invoice's persisted, authoritative flag — stamped at
	// write time when the creating context was bound to a frozen test clock
	// (engine: the subscription is pinned; manual composer: the customer is
	// pinned). Reading it here, instead of re-deriving from the subscription's
	// CURRENT test_clock_id, fixes two defects: (1) manual one-off invoices
	// have no subscription to look through, so the old lookup always returned
	// false and dropped their badge despite simulated timestamps; (2) the badge
	// now survives a later clock unpin, since the old sub.Get was a mutable
	// read-time snapshot (the heuristic feedback_no_heuristic_proxies bans).
	// Stripe-webhook + email events stay wall-clock either way (real-world
	// dispatch time), so they don't carry this flag.
	isSimulated := inv.IsSimulated

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
		IsSimulated: isSimulated,
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
			IsSimulated: isSimulated,
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
			IsSimulated: isSimulated,
		})
	}
	if inv.UncollectibleAt != nil {
		events = append(events, timelineEvent{
			Timestamp:   inv.UncollectibleAt.Format(time.RFC3339),
			Source:      "lifecycle",
			EventType:   "invoice.marked_uncollectible",
			Status:      "canceled",
			Description: "Marked uncollectible — written off as bad debt",
			IsSimulated: isSimulated,
		})
	}
	if inv.PaidAt != nil {
		amt := inv.AmountPaidCents
		desc := "Invoice paid"
		detail := formatPaymentCardDetail(inv.PaymentCardBrand, inv.PaymentCardLast4)
		// Operator-recorded offline payments (cheque/wire/cash) stamp a
		// synthetic out_of_band: payment-intent id — surface them as what
		// they are instead of rendering identically to a card payment.
		if strings.HasPrefix(inv.StripePaymentIntentID, "out_of_band:") {
			desc = "Payment recorded (offline)"
			detail = "Recorded by an operator — cheque, wire, or other out-of-band payment"
		}
		events = append(events, timelineEvent{
			Timestamp:   inv.PaidAt.Format(time.RFC3339),
			Source:      "lifecycle",
			EventType:   "invoice.paid",
			Status:      "succeeded",
			Description: desc,
			AmountCents: &amt,
			Currency:    inv.Currency,
			Detail:      detail,
			IsSimulated: isSimulated,
		})
	}

	// Credit-note chronology rows. The settlement waterfall on the page
	// already shows credit notes channel-by-channel; these rows give the
	// SAME facts a place in the chronology ("Invoice paid" then silence
	// after a refund read as nothing-happened). Issued notes only —
	// drafts aren't activity yet, voided notes vanish from the story the
	// same way Stripe's do. Each row carries the CN's OWN is_simulated:
	// operator-HTTP CNs stamp WALL-CLOCK issued_at (the HTTP path doesn't
	// bind the customer clock) → is_simulated=false → real-time lane;
	// engine clawbacks (downgrade/cancel proration) issue under the
	// clock-pinned sub's bound time → is_simulated=true → billing
	// (Activity) lane, sorted with the other simulated rows. Pre-fix all
	// CNs were tagged with the INVOICE's flag and routed to the real-time
	// lane, so an engine CN showed a simulated timestamp in the wall-clock
	// lane (migration 0117 added the per-CN flag).
	if h.creditNotes != nil {
		if cns, err := h.creditNotes.List(r.Context(), tenantID, inv.ID); err == nil {
			for _, cn := range cns {
				if cn.Status != domain.CreditNoteIssued || cn.IssuedAt == nil {
					continue
				}
				total := cn.TotalCents
				desc := "Credit note issued"
				if cn.RefundAmountCents > 0 && cn.RefundAmountCents == cn.TotalCents {
					desc = "Refund issued"
				} else if cn.RefundAmountCents > 0 {
					desc = "Credit note issued — part refunded to card"
				}
				detail := cn.CreditNoteNumber
				if cn.Reason != "" {
					detail = cn.CreditNoteNumber + " — " + cn.Reason
				}
				events = append(events, timelineEvent{
					Timestamp:   cn.IssuedAt.Format(time.RFC3339),
					Source:      "credit_note",
					EventType:   "credit_note.issued",
					Status:      "succeeded",
					Description: desc,
					AmountCents: &total,
					Currency:    cn.Currency,
					Detail:      detail,
					IsSimulated: cn.IsSimulated,
				})
			}
		}
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
				// Dunning warning + escalation emails are surfaced in the
				// per-customer "Sent emails" section on CustomerDetail
				// (Stripe shape — `docs.stripe.com/invoicing/send-email`
				// lists the email log on the customer page, not the
				// invoice page). Suppressing the rows here avoids the
				// wall-clock-vs-simulated-time visual mismatch in the
				// invoice activity timeline — those rows would show
				// "May 16, 2026" send times next to dunning state rows
				// at simulated cycle dates like "Mar 4, 2025."
				//
				// payment_failed (initial charge) still flows through:
				// foldEmailIntoStripeFailed → mergeFailedPaymentTwins
				// merges it as a "Customer notified by email" sub-line
				// on the dunning_started row (same time domain = both
				// wall-clock from the Stripe webhook side).
				if evt.EmailType == "dunning_warning" || evt.EmailType == "dunning_escalation" {
					continue
				}
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
					if dropCanceledForVoid(inv.VoidedAt, evt.OccurredAt, inv.IsSimulated) {
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
	maxAttemptCount := 0
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
						IsSimulated:  isSimulated,
					})
				}
			}
		}
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

	// Industry-grade timeline consolidation (Stripe / Lago shape):
	// fold downstream-consequence rows into their cause row. Order
	// matters — the email is a consequence of the Stripe failure;
	// the Stripe failure is a consequence of the dunning-scheduled
	// attempt. Fold inside-out so the surviving dunning row inherits
	// the merged Detail ("Customer notified by email") in one pass.
	events = foldEmailIntoStripeFailed(events, 2*time.Minute)
	events = mergeFailedPaymentTwins(events)

	// Sort by timestamp ascending. Use SliceStable so equal-timestamp
	// events preserve insertion order — on a test-clock-pinned sub, the
	// inline charge-fail-then-dunning-start cascade lands at the SAME
	// simulated instant (cycle close) as the invoice's CreatedAt /
	// IssuedAt, so the RFC3339 strings are identical. Unstable sort
	// would render "Automatic retry scheduled" before "Invoice created"
	// — which read as a bug even though it was a sort tiebreak. Events
	// are inserted in lifecycle → email → stripe → dunning order
	// upstream, which is the right rendering order on ties.
	sort.SliceStable(events, func(i, j int) bool {
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

	// One shared context builder across emailed/downloaded/hosted PDFs.
	bt, ci, cnInfos := BuildPDFContext(r.Context(), h.customers, h.settings, h.creditNotes, tenantID, &inv)

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
