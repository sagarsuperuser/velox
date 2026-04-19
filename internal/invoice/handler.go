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

// EmailSender sends invoice-related emails.
type EmailSender interface {
	SendInvoice(to, customerName, invoiceNumber string, totalCents int64, currency string, pdfBytes []byte) error
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
	dunningTimeline DunningTimelineFetcher
	events          domain.EventDispatcher
	emailSender     EmailSender
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
	DunningTimeline DunningTimelineFetcher
	Events          domain.EventDispatcher
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
		h.dunningTimeline = deps[0].DunningTimeline
		h.events = deps[0].Events
	}
	return h
}

// SetEmailSender configures email sending for invoice notifications.
func (h *Handler) SetEmailSender(sender EmailSender) {
	h.emailSender = sender
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
		slog.Error("dispatch invoice event",
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
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "invoice")
		return
	}
	if err != nil {
		respond.Validation(w, r, err.Error())
		return
	}

	respond.JSON(w, r, http.StatusCreated, item)
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
	if h.emailSender != nil && h.customers != nil {
		emailCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 60*time.Second)
		go func() {
			defer cancel()
			cust, err := h.customers.Get(emailCtx, tenantID, inv.CustomerID)
			if err != nil || cust.Email == "" {
				slog.Warn("skip invoice email — cannot resolve customer email",
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
				slog.Warn("skip invoice email — cannot fetch line items",
					"invoice_id", inv.ID, "error", err)
				return
			}
			// RenderPDF is CPU-bound and not ctx-aware. Check ctx before+after
			// so we don't waste a render when the deadline already passed, and
			// so we don't send a stale email if it did.
			if err := emailCtx.Err(); err != nil {
				slog.Warn("skip invoice email — deadline reached before PDF render",
					"invoice_id", inv.ID, "error", err)
				return
			}
			bt := BillToInfo{Name: name, Email: email}
			pdfBytes, err := RenderPDF(inv, items, bt, nil, CompanyInfo{})
			if err != nil {
				slog.Warn("skip invoice email — PDF render failed",
					"invoice_id", inv.ID, "error", err)
				return
			}
			if err := emailCtx.Err(); err != nil {
				slog.Warn("skip invoice email — deadline reached after PDF render",
					"invoice_id", inv.ID, "error", err)
				return
			}
			if err := h.emailSender.SendInvoice(email, name, inv.InvoiceNumber, inv.TotalAmountCents, inv.Currency, pdfBytes); err != nil {
				slog.Error("failed to send invoice email",
					"invoice_id", inv.ID, "email", email, "error", err)
			}
		}()
	}

	// Auto-charge: if customer has a payment method, create PaymentIntent
	if h.charger != nil && h.paymentSetups != nil && inv.AmountDueCents > 0 {
		if ps, err := h.paymentSetups.GetPaymentSetup(r.Context(), tenantID, inv.CustomerID); err == nil &&
			ps.SetupStatus == domain.PaymentSetupReady && ps.StripeCustomerID != "" {
			if charged, err := h.charger.ChargeInvoice(r.Context(), tenantID, inv, ps.StripeCustomerID); err != nil {
				slog.Warn("auto-charge failed, invoice stays finalized",
					"invoice_id", inv.ID, "error", err)
			} else {
				inv = charged
				slog.Info("auto-charge initiated", "invoice_id", inv.ID)
			}
		}
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

	// Cancel Stripe PaymentIntent if one was created
	if h.paymentCancel != nil && inv.StripePaymentIntentID != "" {
		if err := h.paymentCancel.CancelPaymentIntent(r.Context(), inv.StripePaymentIntentID); err != nil {
			slog.Warn("failed to cancel payment intent on void", "invoice_id", id, "pi_id", inv.StripePaymentIntentID, "error", err)
		} else {
			slog.Info("payment intent canceled on void", "invoice_id", id)
		}
	}

	// Reverse any credits that were applied to this invoice
	if h.creditReverser != nil && inv.CustomerID != "" {
		if reversed, err := h.creditReverser.ReverseForInvoice(r.Context(), tenantID, inv.CustomerID, id, inv.InvoiceNumber); err != nil {
			slog.Warn("failed to reverse credits on void", "invoice_id", id, "error", err)
		} else if reversed > 0 {
			slog.Info("credits reversed on invoice void", "invoice_id", id, "reversed_cents", reversed)
		}
	}

	// Resolve any active dunning runs for this invoice
	if h.dunning != nil {
		if err := h.dunning.ResolveByInvoice(r.Context(), tenantID, id, domain.ResolutionManuallyResolved); err != nil {
			slog.Warn("failed to resolve dunning on void", "invoice_id", id, "error", err)
		} else {
			slog.Info("dunning resolved on invoice void", "invoice_id", id)
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

	if h.emailSender == nil {
		respond.Validation(w, r, "email sender not configured")
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
			ci = CompanyInfo{Name: ts.CompanyName, Email: ts.CompanyEmail, Phone: ts.CompanyPhone, Address: ts.CompanyAddress}
		}
	}

	pdfBytes, err := RenderPDF(inv, items, bt, nil, ci)
	if err != nil {
		respond.InternalError(w, r)
		return
	}

	if err := h.emailSender.SendInvoice(body.Email, bt.Name, inv.InvoiceNumber, inv.TotalAmountCents, inv.Currency, pdfBytes); err != nil {
		respond.Validation(w, r, fmt.Sprintf("failed to send email: %s", err.Error()))
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
		respond.Validation(w, r, fmt.Sprintf("payment failed: %s", err.Error()))
		return
	}

	// Resolve any active dunning run — manual collect payment bypasses dunning retry
	if h.dunning != nil {
		if err := h.dunning.ResolveByInvoice(r.Context(), tenantID, id, domain.ResolutionPaymentRecovered); err != nil {
			slog.Warn("failed to resolve dunning after collect payment", "invoice_id", id, "error", err)
		}
	}

	respond.JSON(w, r, http.StatusOK, charged)
}

type timelineEvent struct {
	Timestamp       string `json:"timestamp"`
	Source          string `json:"source"` // "stripe" or "dunning"
	EventType       string `json:"event_type"`
	Status          string `json:"status"`
	Description     string `json:"description"`
	Error           string `json:"error,omitempty"`
	AmountCents     *int64 `json:"amount_cents,omitempty"`
	Currency        string `json:"currency,omitempty"`
	PaymentIntentID string `json:"payment_intent_id,omitempty"`
	AttemptCount    int    `json:"attempt_count,omitempty"`
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

	var events []timelineEvent

	// Fetch Stripe webhook events — only operator-relevant ones
	if h.webhookEvents != nil {
		webhookEvts, err := h.webhookEvents.ListByInvoice(r.Context(), tenantID, id)
		if err == nil {
			for _, evt := range webhookEvts {
				if !relevantStripeEvents[evt.EventType] {
					continue
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

	// Fetch dunning events for this invoice
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
					desc, status := describeDunningEvent(string(evt.EventType), evt.Reason, evt.AttemptCount)
					events = append(events, timelineEvent{
						Timestamp:    evt.CreatedAt.Format(time.RFC3339),
						Source:       "dunning",
						EventType:    string(evt.EventType),
						Status:       status,
						Description:  desc,
						Error:        evt.Reason,
						AttemptCount: evt.AttemptCount,
					})
				}
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

	pdfBytes, err := RenderPDF(inv, items, bt, cnInfos, ci)
	if err != nil {
		respond.InternalError(w, r)
		return
	}

	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", "inline; filename=\""+inv.InvoiceNumber+".pdf\"")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pdfBytes)
}
