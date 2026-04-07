package payment

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// Stripe is the payment adapter. It creates PaymentIntents for finalized invoices
// and processes webhook events to update invoice payment status.
//
// This is NOT an abstract "payment provider" interface. Velox is Stripe-native.
// If we ever support another provider, we'll refactor — not speculate now.
type Stripe struct {
	client   StripeClient
	invoices InvoiceUpdater
	webhooks WebhookStore
}

// StripeClient is the interface for Stripe API calls.
// In production this wraps stripe-go; in tests it's a mock.
type StripeClient interface {
	CreatePaymentIntent(ctx context.Context, params PaymentIntentParams) (PaymentIntentResult, error)
	CancelPaymentIntent(ctx context.Context, paymentIntentID string) error
}

type PaymentIntentParams struct {
	AmountCents       int64
	Currency          string
	CustomerID        string // Stripe customer ID
	Description       string
	IdempotencyKey    string
	Metadata          map[string]string
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
	GetByStripePaymentIntentID(ctx context.Context, tenantID, stripePaymentIntentID string) (domain.Invoice, error)
}

// WebhookStore persists Stripe webhook events for audit trail.
type WebhookStore interface {
	IngestEvent(ctx context.Context, tenantID string, event domain.StripeWebhookEvent) (domain.StripeWebhookEvent, bool, error)
}

func NewStripe(client StripeClient, invoices InvoiceUpdater, webhooks WebhookStore) *Stripe {
	return &Stripe{client: client, invoices: invoices, webhooks: webhooks}
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
	result, err := s.client.CreatePaymentIntent(ctx, PaymentIntentParams{
		AmountCents:    inv.AmountDueCents,
		Currency:       inv.Currency,
		CustomerID:     stripeCustomerID,
		Description:    fmt.Sprintf("Invoice %s", inv.InvoiceNumber),
		IdempotencyKey: fmt.Sprintf("velox_inv_%s", inv.ID),
		Metadata: map[string]string{
			"velox_invoice_id":     inv.ID,
			"velox_invoice_number": inv.InvoiceNumber,
			"velox_tenant_id":      tenantID,
			"velox_customer_id":    inv.CustomerID,
		},
	})
	if err != nil {
		// Record the failure but don't crash — the invoice stays finalized
		s.invoices.UpdatePayment(ctx, tenantID, inv.ID, domain.PaymentFailed, "", err.Error(), nil)
		return domain.Invoice{}, fmt.Errorf("create payment intent: %w", err)
	}

	slog.Info("payment intent created",
		"invoice_id", inv.ID,
		"payment_intent_id", result.ID,
		"amount_cents", inv.AmountDueCents,
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
	if err != nil {
		return fmt.Errorf("find invoice for PI %s: %w", event.PaymentIntentID, err)
	}

	now := time.Now().UTC()

	// Mark payment as succeeded
	if _, err := s.invoices.UpdatePayment(ctx, tenantID, inv.ID,
		domain.PaymentSucceeded, event.PaymentIntentID, "", &now); err != nil {
		return fmt.Errorf("update payment status: %w", err)
	}

	// Transition invoice to paid
	if _, err := s.invoices.UpdateStatus(ctx, tenantID, inv.ID, domain.InvoicePaid); err != nil {
		return fmt.Errorf("update invoice status: %w", err)
	}

	slog.Info("payment succeeded",
		"invoice_id", inv.ID,
		"payment_intent_id", event.PaymentIntentID,
	)

	return nil
}

func (s *Stripe) handlePaymentFailed(ctx context.Context, tenantID string, event domain.StripeWebhookEvent) error {
	if event.PaymentIntentID == "" {
		return fmt.Errorf("payment_intent.payment_failed event missing payment_intent_id")
	}

	inv, err := s.invoices.GetByStripePaymentIntentID(ctx, tenantID, event.PaymentIntentID)
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

	return nil
}
