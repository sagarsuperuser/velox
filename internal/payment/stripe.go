package payment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/payment/breaker"
)

// Stripe is the payment adapter. It creates PaymentIntents for finalized invoices
// and processes webhook events to update invoice payment status.
//
// This is NOT an abstract "payment provider" interface. Velox is Stripe-native.
// If we ever support another provider, we'll refactor — not speculate now.
// DunningStarter starts dunning for failed payments.
type DunningStarter interface {
	StartDunning(ctx context.Context, tenantID string, invoiceID, customerID string) (domain.InvoiceDunningRun, error)
}

// CardDetails holds card info fetched from Stripe for display.
type CardDetails struct {
	PaymentMethodID string
	Brand           string
	Last4           string
	ExpMonth        int
	ExpYear         int
}

// CardFetcher fetches card details from Stripe for a customer.
type CardFetcher interface {
	FetchCardDetails(ctx context.Context, stripeCustomerID string) (CardDetails, error)
}

// EmailReceipt sends payment receipt emails.
type EmailReceipt interface {
	SendPaymentReceipt(to, customerName, invoiceNumber string, amountCents int64, currency string) error
}

// CustomerEmailResolver resolves customer contact info for email notifications.
type CustomerEmailResolver interface {
	GetCustomerEmail(ctx context.Context, tenantID, customerID string) (email, displayName string, err error)
}

// EmailPaymentUpdate sends payment update request emails.
type EmailPaymentUpdate interface {
	SendPaymentUpdateRequest(to, customerName, invoiceNumber string, amountDueCents int64, currency, updateURL string) error
}

type Stripe struct {
	client             StripeClient
	invoices           InvoiceUpdater
	webhooks           WebhookStore
	dunning            DunningStarter
	paymentSetups      PaymentSetupStore
	cardFetcher        CardFetcher
	events             domain.EventDispatcher
	emailReceipt       EmailReceipt
	customerEmail      CustomerEmailResolver
	emailPaymentUpdate EmailPaymentUpdate
	paymentUpdateURL   string
	tokenSvc           *TokenService
	breaker            *breaker.Breaker // optional; nil = no breaker
}

// StripeClient is the interface for Stripe API calls.
// In production this wraps stripe-go; in tests it's a mock.
type StripeClient interface {
	CreatePaymentIntent(ctx context.Context, params PaymentIntentParams) (PaymentIntentResult, error)
	CancelPaymentIntent(ctx context.Context, paymentIntentID string) error
	GetPaymentIntent(ctx context.Context, paymentIntentID string) (PaymentIntentResult, error)
}

type PaymentIntentParams struct {
	AmountCents    int64
	Currency       string
	CustomerID     string // Stripe customer ID
	Description    string
	IdempotencyKey string
	Metadata       map[string]string
}

type PaymentIntentResult struct {
	ID           string
	Status       string
	ClientSecret string
}

// InvoiceUpdater updates invoice payment status.
type InvoiceUpdater interface {
	UpdatePayment(ctx context.Context, tenantID, id string, paymentStatus domain.InvoicePaymentStatus, stripePaymentIntentID, lastPaymentError string, paidAt *time.Time) (domain.Invoice, error)
	UpdateStatus(ctx context.Context, tenantID, id string, status domain.InvoiceStatus) (domain.Invoice, error)
	MarkPaid(ctx context.Context, tenantID, id string, stripePaymentIntentID string, paidAt time.Time) (domain.Invoice, error)
	GetByStripePaymentIntentID(ctx context.Context, tenantID, stripePaymentIntentID string) (domain.Invoice, error)
	Get(ctx context.Context, tenantID, id string) (domain.Invoice, error)
}

// WebhookStore persists and queries Stripe webhook events.
type WebhookStore interface {
	IngestEvent(ctx context.Context, tenantID string, event domain.StripeWebhookEvent) (domain.StripeWebhookEvent, bool, error)
	ListByInvoice(ctx context.Context, tenantID, invoiceID string) ([]domain.StripeWebhookEvent, error)
}

func NewStripe(client StripeClient, invoices InvoiceUpdater, webhooks WebhookStore, paymentSetups PaymentSetupStore, dunning ...DunningStarter) *Stripe {
	s := &Stripe{client: client, invoices: invoices, webhooks: webhooks, paymentSetups: paymentSetups}
	if len(dunning) > 0 {
		s.dunning = dunning[0]
	}
	return s
}

// SetCardFetcher configures card detail fetching from Stripe.
func (s *Stripe) SetCardFetcher(cf CardFetcher) {
	s.cardFetcher = cf
}

// SetEmailReceipt configures payment receipt email sending.
func (s *Stripe) SetEmailReceipt(receipt EmailReceipt, customerEmail CustomerEmailResolver) {
	s.emailReceipt = receipt
	s.customerEmail = customerEmail
}

// SetEmailPaymentUpdate configures payment update request email sending.
func (s *Stripe) SetEmailPaymentUpdate(sender EmailPaymentUpdate, customerEmail CustomerEmailResolver, paymentUpdateURL string) {
	s.emailPaymentUpdate = sender
	if s.customerEmail == nil {
		s.customerEmail = customerEmail
	}
	s.paymentUpdateURL = paymentUpdateURL
}

// SetTokenService configures the payment update token service for tokenized update links.
func (s *Stripe) SetTokenService(svc *TokenService) {
	s.tokenSvc = svc
}

// SetEventDispatcher configures outbound webhook event firing.
func (s *Stripe) SetEventDispatcher(events domain.EventDispatcher) {
	s.events = events
}

// SetBreaker wires a per-tenant circuit breaker around Stripe calls. When
// set, ChargeInvoice short-circuits with breaker.ErrOpen if the tenant's
// breaker is rejecting — the Stripe call is not made and the invoice
// state is left untouched so dunning treats it as "try later" without
// ticking attempt_count.
func (s *Stripe) SetBreaker(b *breaker.Breaker) {
	s.breaker = b
}

// IsUnknownPaymentFailure classifies an error returned from ChargeInvoice
// as a Stripe-side unknown outcome (5xx, timeout, network) — the failure
// category that should feed the circuit breaker. Explicit card declines
// and validation errors return false. Exported so the breaker factory can
// reuse the exact same classification used at the charge site.
func IsUnknownPaymentFailure(err error) bool {
	if err == nil {
		return false
	}
	var pe *PaymentError
	if errors.As(err, &pe) {
		return pe.Unknown
	}
	// Plain error with no classification — assume unknown to be conservative.
	// A wrapped non-PaymentError from our own code shouldn't happen; if it
	// does we'd rather trip the breaker than miss a real incident.
	return true
}

func (s *Stripe) fireEvent(ctx context.Context, tenantID, eventType string, payload map[string]any) {
	if s.events == nil {
		return
	}
	go func() {
		_ = s.events.Dispatch(ctx, tenantID, eventType, payload)
	}()
}

// ChargeInvoice creates a Stripe PaymentIntent for a finalized invoice.
// The invoice must have a customer with a Stripe customer ID already set up.
func (s *Stripe) ChargeInvoice(ctx context.Context, tenantID string, inv domain.Invoice, stripeCustomerID string) (domain.Invoice, error) {
	if inv.Status != domain.InvoiceFinalized {
		return domain.Invoice{}, fmt.Errorf("can only charge finalized invoices, current status: %s", inv.Status)
	}
	if inv.AmountDueCents <= 0 {
		return domain.Invoice{}, fmt.Errorf("invoice has no amount due")
	}
	if stripeCustomerID == "" {
		return domain.Invoice{}, fmt.Errorf("stripe customer ID is required")
	}

	// Idempotency: use invoice ID as the key so retries don't create duplicate PIs
	metadata := map[string]string{
		"velox_invoice_id":     inv.ID,
		"velox_invoice_number": inv.InvoiceNumber,
		"velox_tenant_id":      tenantID,
		"velox_customer_id":    inv.CustomerID,
	}
	// Propagate the Velox request ID so operators can correlate Velox logs to
	// Stripe dashboard events via metadata search. Absent from scheduler-driven
	// charges (no HTTP request → empty ID), which is fine.
	if reqID := chimw.GetReqID(ctx); reqID != "" {
		metadata["velox_request_id"] = reqID
	}
	params := PaymentIntentParams{
		AmountCents:    inv.AmountDueCents,
		Currency:       inv.Currency,
		CustomerID:     stripeCustomerID,
		Description:    fmt.Sprintf("Invoice %s", inv.InvoiceNumber),
		IdempotencyKey: fmt.Sprintf("velox_inv_%s", inv.ID),
		Metadata:       metadata,
	}

	var result PaymentIntentResult
	var err error
	if s.breaker != nil {
		var out any
		out, err = s.breaker.Execute(ctx, tenantID, func(ctx context.Context) (any, error) {
			return s.client.CreatePaymentIntent(ctx, params)
		})
		if errors.Is(err, breaker.ErrOpen) {
			// Breaker short-circuit: the call to Stripe was not made. Do NOT
			// mutate invoice state — leaving payment_status as-is lets the
			// scheduler try again on the next tick once cooldown elapses.
			// Returning a distinct sentinel lets dunning treat this as a
			// transient skip rather than ticking attempt_count.
			return domain.Invoice{}, ErrPaymentTransient
		}
		if out != nil {
			result = out.(PaymentIntentResult)
		}
	} else {
		result, err = s.client.CreatePaymentIntent(ctx, params)
	}
	if err != nil {
		// Categorise the error so ambiguous (5xx/timeout/network) outcomes are
		// recorded as PaymentUnknown, not PaymentFailed. Retrying a failed-
		// but-actually-succeeded charge would double-bill the customer; the
		// reconciler resolves unknowns by querying Stripe later.
		//
		// Dunning is NOT started here. With Confirm:true + OffSession:true,
		// Stripe creates the PI even on decline and sends
		// payment_intent.payment_failed, which triggers dunning via
		// handlePaymentFailed(). Starting dunning here would duplicate.
		var pe *PaymentError
		if !errors.As(err, &pe) {
			pe = &PaymentError{Message: errs.Scrub(err.Error()), Unknown: true}
		}

		status := domain.PaymentFailed
		verb := "payment failed"
		if pe.Unknown {
			status = domain.PaymentUnknown
			verb = "payment state unknown"
			slog.Warn("payment intent outcome unknown — reconciler will resolve",
				"invoice_id", inv.ID,
				"stripe_payment_intent_id", pe.PaymentIntentID,
				"error", pe.Message,
			)
		}
		_, _ = s.invoices.UpdatePayment(ctx, tenantID, inv.ID, status, pe.PaymentIntentID, pe.Message, nil)
		return domain.Invoice{}, fmt.Errorf("%s: %s", verb, pe.Message)
	}

	slog.Info("payment intent created",
		"invoice_id", inv.ID,
		"payment_intent_id", result.ID,
		"amount_cents", inv.AmountDueCents,
		"request_id", chimw.GetReqID(ctx),
	)

	// Update invoice with PI reference and set to processing
	return s.invoices.UpdatePayment(ctx, tenantID, inv.ID,
		domain.PaymentProcessing, result.ID, "", nil)
}

// HandleWebhook processes a Stripe webhook event and updates the corresponding invoice.
func (s *Stripe) HandleWebhook(ctx context.Context, tenantID string, event domain.StripeWebhookEvent) error {
	// Persist the event for audit trail (idempotent — returns false if already seen)
	_, isNew, err := s.webhooks.IngestEvent(ctx, tenantID, event)
	if err != nil {
		return fmt.Errorf("ingest webhook event: %w", err)
	}
	if !isNew {
		slog.Info("duplicate webhook event, skipping", "stripe_event_id", event.StripeEventID)
		return nil
	}

	switch event.EventType {
	case "payment_intent.succeeded":
		return s.handlePaymentSucceeded(ctx, tenantID, event)
	case "payment_intent.payment_failed":
		return s.handlePaymentFailed(ctx, tenantID, event)
	case "checkout.session.completed":
		return s.handleCheckoutCompleted(ctx, tenantID, event)
	default:
		slog.Debug("unhandled webhook event type", "type", event.EventType)
		return nil
	}
}

func (s *Stripe) handlePaymentSucceeded(ctx context.Context, tenantID string, event domain.StripeWebhookEvent) error {
	if event.PaymentIntentID == "" {
		return fmt.Errorf("payment_intent.succeeded event missing payment_intent_id")
	}

	inv, err := s.invoices.GetByStripePaymentIntentID(ctx, tenantID, event.PaymentIntentID)
	if err != nil && event.InvoiceID != "" {
		inv, err = s.invoices.Get(ctx, tenantID, event.InvoiceID)
	}
	if err != nil {
		return fmt.Errorf("find invoice for PI %s: %w", event.PaymentIntentID, err)
	}

	now := time.Now().UTC()

	// Single atomic operation: mark paid, zero amount_due, record PI + paid_at
	if _, err := s.invoices.MarkPaid(ctx, tenantID, inv.ID, event.PaymentIntentID, now); err != nil {
		return fmt.Errorf("mark invoice paid: %w", err)
	}

	slog.Info("payment succeeded",
		"invoice_id", inv.ID,
		"payment_intent_id", event.PaymentIntentID,
	)

	s.fireEvent(ctx, tenantID, domain.EventPaymentSucceeded, map[string]any{
		"invoice_id":        inv.ID,
		"customer_id":       inv.CustomerID,
		"payment_intent_id": event.PaymentIntentID,
		"amount_cents":      inv.TotalAmountCents,
		"currency":          inv.Currency,
	})

	// Send payment receipt email asynchronously
	if s.emailReceipt != nil && s.customerEmail != nil {
		go func() {
			email, name, err := s.customerEmail.GetCustomerEmail(ctx, tenantID, inv.CustomerID)
			if err != nil || email == "" {
				slog.Warn("skip payment receipt email — cannot resolve customer email",
					"invoice_id", inv.ID, "customer_id", inv.CustomerID, "error", err)
				return
			}
			if err := s.emailReceipt.SendPaymentReceipt(email, name, inv.InvoiceNumber, inv.TotalAmountCents, inv.Currency); err != nil {
				slog.Error("failed to send payment receipt email",
					"invoice_id", inv.ID, "email", email, "error", err)
			}
		}()
	}

	return nil
}

func (s *Stripe) handlePaymentFailed(ctx context.Context, tenantID string, event domain.StripeWebhookEvent) error {
	if event.PaymentIntentID == "" {
		return fmt.Errorf("payment_intent.payment_failed event missing payment_intent_id")
	}

	// Try to find invoice by PI ID first, fall back to invoice ID from metadata
	inv, err := s.invoices.GetByStripePaymentIntentID(ctx, tenantID, event.PaymentIntentID)
	if err != nil && event.InvoiceID != "" {
		inv, err = s.invoices.Get(ctx, tenantID, event.InvoiceID)
	}
	if err != nil {
		return fmt.Errorf("find invoice for PI %s: %w", event.PaymentIntentID, err)
	}

	failureMsg := event.FailureMessage
	if failureMsg == "" {
		failureMsg = "payment failed"
	}

	if _, err := s.invoices.UpdatePayment(ctx, tenantID, inv.ID,
		domain.PaymentFailed, event.PaymentIntentID, failureMsg, nil); err != nil {
		return fmt.Errorf("update payment status: %w", err)
	}

	slog.Info("payment failed",
		"invoice_id", inv.ID,
		"payment_intent_id", event.PaymentIntentID,
		"failure_message", failureMsg,
	)

	s.fireEvent(ctx, tenantID, domain.EventPaymentFailed, map[string]any{
		"invoice_id":        inv.ID,
		"customer_id":       inv.CustomerID,
		"payment_intent_id": event.PaymentIntentID,
		"failure_message":   failureMsg,
		"amount_cents":      inv.TotalAmountCents,
		"currency":          inv.Currency,
	})

	// Auto-start dunning for failed payments
	if s.dunning != nil {
		if _, err := s.dunning.StartDunning(ctx, tenantID, inv.ID, inv.CustomerID); err != nil {
			slog.Warn("failed to start dunning",
				"invoice_id", inv.ID,
				"error", err,
			)
			// Non-fatal: invoice is already marked failed, dunning can be started manually
		} else {
			slog.Info("dunning started for failed payment", "invoice_id", inv.ID)
		}
	}

	// Send payment update request email asynchronously with tokenized URL
	if s.emailPaymentUpdate != nil && s.customerEmail != nil && s.paymentUpdateURL != "" {
		go func() {
			email, name, err := s.customerEmail.GetCustomerEmail(ctx, tenantID, inv.CustomerID)
			if err != nil || email == "" {
				slog.Warn("skip payment update email — cannot resolve customer email",
					"invoice_id", inv.ID, "customer_id", inv.CustomerID, "error", err)
				return
			}

			// Generate a secure token for the payment update link
			var updateURL string
			if s.tokenSvc != nil {
				rawToken, err := s.tokenSvc.Create(ctx, tenantID, inv.CustomerID, inv.ID)
				if err != nil {
					slog.Error("failed to create payment update token",
						"invoice_id", inv.ID, "error", err)
					return
				}
				updateURL = fmt.Sprintf("%s/%s", s.paymentUpdateURL, rawToken)
			} else {
				updateURL = fmt.Sprintf("%s?invoice_id=%s&customer_id=%s", s.paymentUpdateURL, inv.ID, inv.CustomerID)
			}

			if err := s.emailPaymentUpdate.SendPaymentUpdateRequest(email, name, inv.InvoiceNumber, inv.AmountDueCents, inv.Currency, updateURL); err != nil {
				slog.Error("failed to send payment update email",
					"invoice_id", inv.ID, "email", email, "error", err)
			}
		}()
	}

	return nil
}

func (s *Stripe) handleCheckoutCompleted(ctx context.Context, tenantID string, event domain.StripeWebhookEvent) error {
	// Extract velox_customer_id from metadata (set during checkout session creation)
	customerID := ""
	if payload, ok := event.Payload["raw"]; ok {
		var raw struct {
			Data struct {
				Object struct {
					Customer string            `json:"customer"`
					Metadata map[string]string `json:"metadata"`
				} `json:"object"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(payload.(string)), &raw); err == nil {
			customerID = raw.Data.Object.Metadata["velox_customer_id"]
			if event.CustomerExternalID == "" {
				event.CustomerExternalID = raw.Data.Object.Customer
			}
		}
	}

	if customerID == "" {
		slog.Debug("checkout.session.completed missing velox_customer_id, skipping")
		return nil
	}

	if s.paymentSetups == nil {
		slog.Warn("payment setup store not configured, cannot update payment status")
		return nil
	}

	now := time.Now().UTC()
	setup := domain.CustomerPaymentSetup{
		CustomerID:                  customerID,
		TenantID:                    tenantID,
		SetupStatus:                 domain.PaymentSetupReady,
		DefaultPaymentMethodPresent: true,
		PaymentMethodType:           "card",
		StripeCustomerID:            event.CustomerExternalID,
		LastVerifiedAt:              &now,
		UpdatedAt:                   now,
	}

	// Fetch card details from Stripe for display
	if s.cardFetcher != nil && event.CustomerExternalID != "" {
		if card, err := s.cardFetcher.FetchCardDetails(ctx, event.CustomerExternalID); err == nil {
			setup.CardBrand = card.Brand
			setup.CardLast4 = card.Last4
			setup.CardExpMonth = card.ExpMonth
			setup.CardExpYear = card.ExpYear
			setup.StripePaymentMethodID = card.PaymentMethodID
		} else {
			slog.Warn("failed to fetch card details", "stripe_customer_id", event.CustomerExternalID, "error", err)
		}
	}

	_, err := s.paymentSetups.UpsertPaymentSetup(ctx, tenantID, setup)
	if err != nil {
		return fmt.Errorf("update payment setup: %w", err)
	}

	slog.Info("payment method setup completed",
		"customer_id", customerID,
		"stripe_customer_id", event.CustomerExternalID,
		"card_brand", setup.CardBrand,
		"card_last4", setup.CardLast4,
	)

	s.fireEvent(ctx, tenantID, "payment_method.updated", map[string]any{
		"customer_id":        customerID,
		"stripe_customer_id": event.CustomerExternalID,
		"card_brand":         setup.CardBrand,
		"card_last4":         setup.CardLast4,
	})

	return nil
}
