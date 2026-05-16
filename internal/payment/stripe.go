package payment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	chimw "github.com/go-chi/chi/v5/middleware"

	mw "github.com/sagarsuperuser/velox/internal/api/middleware"
	veloxauth "github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/payment/breaker"
	"github.com/sagarsuperuser/velox/internal/platform/clock"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

// Stripe is the payment adapter. It creates PaymentIntents for finalized invoices
// and processes webhook events to update invoice payment status.
//
// This is NOT an abstract "payment provider" interface. Velox is Stripe-native.
// If we ever support another provider, we'll refactor — not speculate now.
// DunningStarter starts dunning for failed payments.
type DunningStarter interface {
	StartDunning(ctx context.Context, tenantID string, invoiceID, customerID string, failureAt time.Time) (domain.InvoiceDunningRun, error)
}

// CardDetails holds card info fetched from Stripe for display.
type CardDetails struct {
	PaymentMethodID string
	Brand           string
	Last4           string
	ExpMonth        int
	ExpYear         int
}

// CardFetcher fetches card details from Stripe.
//
// FetchCardDetails returns the customer's *default* PM card. Used
// by dunning to render "we'll retry charging Visa •••• 4242 in 3
// days" copy.
//
// FetchCardForPaymentIntent returns the card actually charged on a
// specific PaymentIntent. Used by the payment-succeeded webhook
// handler to stamp the invoice's `payment_card_brand` /
// `payment_card_last4` columns (ADR-020). Works for one-off
// Checkout cards too — the customer doesn't have to save the PM
// for the timeline to show what was charged.
type CardFetcher interface {
	FetchCardDetails(ctx context.Context, stripeCustomerID string) (CardDetails, error)
	FetchCardForPaymentIntent(ctx context.Context, paymentIntentID string) (CardDetails, error)
}

// EmailReceipt sends payment receipt emails. ctx carries livemode so
// the enqueue/brand lookup runs against the right tenant_settings row.
type EmailReceipt interface {
	SendPaymentReceipt(ctx context.Context, tenantID, to, customerName, invoiceNumber string, amountCents int64, currency, publicToken string) error
}

// CustomerEmailResolver resolves customer contact info for email notifications.
type CustomerEmailResolver interface {
	GetCustomerEmail(ctx context.Context, tenantID, customerID string) (email, displayName string, err error)
}

// EmailPaymentFailed sends "your charge was declined" emails. Hits
// after a Stripe charge attempt actually went through and was
// declined — distinct from EmailPaymentSetup which fires when there's
// no PM on file at finalize (no charge was ever attempted). Same
// signature as dunning.EmailNotifier so OutboxSender satisfies both.
type EmailPaymentFailed interface {
	SendPaymentFailed(ctx context.Context, tenantID, to, customerName, invoiceNumber, reason, publicToken string) error
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
	emailPaymentFailed EmailPaymentFailed
	breaker            *breaker.Breaker // optional; nil = no breaker
	pmAttacher         PaymentMethodAttacher
	resolver           clock.Resolver // optional; binds effective-now from invoice
}

// SetResolver wires the unified clock.Resolver. Webhook handlers fire
// in their own goroutine with no inherited ctx binding — they bind
// effective-now from the invoice they're processing so MarkPaid /
// UpdatePayment / StartDunning all stamp simulated time on
// clock-pinned invoices. Without this, webhook-driven writes leak
// wall-clock back into the simulation. Optional: nil leaves binding
// off (test default).
func (s *Stripe) SetResolver(r clock.Resolver) {
	s.resolver = r
}

// bindForInvoice resolves effective-now from the invoice and binds
// ctx for downstream stamps. Returns ctx unchanged on resolver error
// or no resolver — webhook handlers must never fail their inbound
// event on a transient resolution issue.
func (s *Stripe) bindForInvoice(ctx context.Context, tenantID, invoiceID string) context.Context {
	bound, _ := clock.BindEffectiveNow(ctx, s.resolver, clock.Pin{TenantID: tenantID, InvoiceID: invoiceID})
	return bound
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
	// SetPaymentCard stamps the card brand + last4 used to settle
	// an invoice. Optional — empty values render no sub-line in
	// the timeline. Called by handlePaymentSucceeded after MarkPaid
	// lands. ADR-020.
	SetPaymentCard(ctx context.Context, tenantID, id, brand, last4 string) error
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

// SetEmailPaymentFailed configures the post-decline email sender.
// Mirrors the dunning email path — points the customer at the hosted
// invoice URL where they can update PM and retry. The customer-email
// resolver is shared with the receipt path.
func (s *Stripe) SetEmailPaymentFailed(sender EmailPaymentFailed, customerEmail CustomerEmailResolver) {
	s.emailPaymentFailed = sender
	if s.customerEmail == nil {
		s.customerEmail = customerEmail
	}
}

// SetEventDispatcher configures outbound webhook event firing.
func (s *Stripe) SetEventDispatcher(events domain.EventDispatcher) {
	s.events = events
}

// SetBreaker wires a global circuit breaker around Stripe calls. When
// set, ChargeInvoice short-circuits with breaker.ErrOpen if the breaker
// is rejecting — the Stripe call is not made and the invoice state is
// left untouched so dunning treats it as "try later" without ticking
// attempt_count.
func (s *Stripe) SetBreaker(b *breaker.Breaker) {
	s.breaker = b
}

// PaymentMethodAttacher persists a PM row after a Stripe setup_intent
// succeeds. Optional — if nil, setup_intent.succeeded events are logged
// and ack'd but no payment_methods row is created. In practice the
// router always wires paymentmethods.Service here.
//
// The method name is AttachForWebhook (not AttachFromSetupIntent) so
// paymentmethods.Service can keep its richer AttachFromSetupIntent
// method — returning (PaymentMethod, error) — for tests and direct
// callers, while this package imports nothing from paymentmethods.
type PaymentMethodAttacher interface {
	AttachForWebhook(ctx context.Context, tenantID, customerID, stripePaymentMethodID string) error
}

// SetPaymentMethodAttacher configures the handler that processes
// setup_intent.succeeded events.
func (s *Stripe) SetPaymentMethodAttacher(a PaymentMethodAttacher) {
	s.pmAttacher = a
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

// fireEvent dispatches an outbound webhook event. Synchronous by design: when
// the outbox path is enabled (RES-1, the default), Dispatch is a short DB
// insert, and persisting-before-return is what the outbox exists to
// guarantee. Errors are logged but not surfaced — the business op has
// already committed, and we prefer that a missed event shows up in logs
// over failing the end-user request for a webhook side-effect.
func (s *Stripe) fireEvent(ctx context.Context, tenantID, eventType string, payload map[string]any) {
	if s.events == nil {
		return
	}
	if err := s.events.Dispatch(ctx, tenantID, eventType, payload); err != nil {
		slog.Error("dispatch event",
			"event_type", eventType,
			"tenant_id", tenantID,
			"error", err,
		)
	}
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

	// Seed tenantID on ctx so the per-tenant Stripe client resolver
	// (payment.StripeClients) can look it up. Scheduler / billing-engine
	// driven charges arrive on background ctx without tenant stamped.
	// Livemode is already carried on ctx by authenticated callers; for
	// background workers it defaults to live (postgres.Livemode default).
	ctx = veloxauth.WithTenantID(ctx, tenantID)

	// Stable business metadata only — nothing per-HTTP-request. The
	// idempotency key + metadata together form Stripe's dedup contract:
	// same key + same params → cached response; different params → 409
	// conflict. Putting velox_request_id (per-call chi ID) in metadata
	// would make every retry attempt see "same key, different params"
	// and 409. Log correlation goes through stripe_payment_intent_id
	// (recorded back on the invoice) instead.
	metadata := map[string]string{
		"velox_invoice_id":     inv.ID,
		"velox_invoice_number": inv.InvoiceNumber,
		"velox_tenant_id":      tenantID,
		"velox_customer_id":    inv.CustomerID,
	}
	// Per-attempt idempotency key. inv.UpdatedAt advances every time
	// UpdatePayment runs (which happens on every prior failed attempt
	// recording last_payment_error), so each genuine retry gets a
	// fresh key — Stripe creates a new PI rather than returning the
	// cached failed one. Within a single attempt, two concurrent
	// callers (scheduler + operator click) read the same UpdatedAt
	// and dedupe correctly: Stripe returns the same PI to both, no
	// double-charge.
	idempotencyKey := fmt.Sprintf("velox_inv_%s_%d", inv.ID, inv.UpdatedAt.UnixNano())
	params := PaymentIntentParams{
		AmountCents:    inv.AmountDueCents,
		Currency:       inv.Currency,
		CustomerID:     stripeCustomerID,
		Description:    fmt.Sprintf("Invoice %s", inv.InvoiceNumber),
		IdempotencyKey: idempotencyKey,
		Metadata:       metadata,
	}

	var result PaymentIntentResult
	var err error
	if s.breaker != nil {
		var out any
		out, err = s.breaker.Execute(ctx, func(ctx context.Context) (any, error) {
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
		// Count unknown outcomes as failed for the success-rate alert — a
		// Stripe outage genuinely impairs customer charging and should page,
		// even though the reconciler will later resolve the per-invoice state.
		mw.RecordPaymentCharge("failed")

		// Inline StartDunning for known-failed charges so the dunning run
		// exists by the time the orchestrator's Phase 5 queries due runs
		// in the same Advance. Pre-fix this was deferred to the
		// payment_intent.payment_failed webhook — fine on wall-clock, but
		// under test-clock catchup the webhook arrives AFTER Phase 5 has
		// already exited (Phase 3 fires PI, Phase 5 runs immediately,
		// webhook lands later), so the new dunning run sat at
		// attempt_count=0 with no retries fired until the next Advance.
		// Industry parity: Stripe Test Clocks processes the failure
		// synchronously inside the advance.
		//
		// Safe to call alongside the webhook path: StartDunning is
		// idempotent by invoice (migration 0085 UNIQUE), so the
		// subsequent webhook-driven call returns the existing run.
		//
		// Only fires on definitively-failed charges. Unknown outcomes
		// (Stripe outage, ambiguous timeout) defer to the webhook so
		// the reconciler can resolve them without burning a retry on
		// an ambiguous result.
		if !pe.Unknown && s.dunning != nil {
			failureAt := simulatedFailureAt(inv)
			if _, derr := s.dunning.StartDunning(ctx, tenantID, inv.ID, inv.CustomerID, failureAt); derr != nil {
				slog.Warn("inline StartDunning after known-failed charge", "invoice_id", inv.ID, "error", derr)
			}
		}

		// Wrap with %w so respond.FromError can detect *PaymentError
		// via errors.As and surface OperatorSafeMessage() instead of
		// leaking pe.Message (which is the raw Stripe SDK string —
		// includes idempotency-key conflicts, validation errors, etc.).
		// ADR-026.
		return domain.Invoice{}, fmt.Errorf("%s: %w", verb, pe)
	}

	slog.Info("payment intent created",
		"invoice_id", inv.ID,
		"payment_intent_id", result.ID,
		"amount_cents", inv.AmountDueCents,
		"request_id", chimw.GetReqID(ctx),
	)
	mw.RecordPaymentCharge("succeeded")

	// Update invoice with PI reference and set to processing
	return s.invoices.UpdatePayment(ctx, tenantID, inv.ID,
		domain.PaymentProcessing, result.ID, "", nil)
}

// simulatedFailureAt derives the cycle-close instant for a failed
// charge on the given invoice. Used by both the inline charge-failure
// path and the async webhook handler so dunning anchors next_action_at
// on simulated time even when running under test-clock catchup.
//
// Returns the latest invoice period boundary at or before IssuedAt:
// - in_arrears: BillingPeriodEnd (= elapsed period close = cycle fire)
// - in_advance: BillingPeriodStart (= upcoming period start = cycle fire)
// Both directions converge on "the cycle close instant in simulated
// time." Manual invoices with no period fields fall back to IssuedAt,
// and ultimately to time.Now() — both wall-clock-equivalent in
// production.
func simulatedFailureAt(inv domain.Invoice) time.Time {
	failureAt := time.Now()
	if inv.IssuedAt != nil {
		failureAt = *inv.IssuedAt
	}
	var candidate time.Time
	if !inv.BillingPeriodStart.IsZero() && !inv.BillingPeriodStart.After(failureAt) && inv.BillingPeriodStart.After(candidate) {
		candidate = inv.BillingPeriodStart
	}
	if !inv.BillingPeriodEnd.IsZero() && !inv.BillingPeriodEnd.After(failureAt) && inv.BillingPeriodEnd.After(candidate) {
		candidate = inv.BillingPeriodEnd
	}
	if !candidate.IsZero() {
		failureAt = candidate
	}
	return failureAt
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
	case "setup_intent.succeeded":
		return s.handleSetupIntentSucceeded(ctx, tenantID, event)
	case "setup_intent.setup_failed":
		// We don't persist anything for setup_failed — Stripe Elements
		// already surfaced the error to the customer browser. Just log
		// so operators can tell this happened from event history.
		slog.Info("setup_intent.setup_failed",
			"stripe_event_id", event.StripeEventID,
			"failure_message", event.FailureMessage)
		return nil
	default:
		slog.Debug("unhandled webhook event type", "type", event.EventType)
		return nil
	}
}

// handleSetupIntentSucceeded re-parses the raw payload to pull out the
// velox_customer_id + payment_method fields, then delegates to the
// PaymentMethodAttacher. Re-parsing (instead of adding fields to
// StripeWebhookEvent) keeps the event struct narrow — other consumers
// of the event don't need to know about setup-intent-specific fields.
func (s *Stripe) handleSetupIntentSucceeded(ctx context.Context, tenantID string, event domain.StripeWebhookEvent) error {
	if s.pmAttacher == nil {
		slog.Debug("setup_intent.succeeded: no PaymentMethodAttacher wired, skipping")
		return nil
	}

	raw, ok := event.Payload["raw"].(string)
	if !ok {
		return fmt.Errorf("setup_intent.succeeded: missing raw payload")
	}
	var parsed struct {
		Data struct {
			Object struct {
				ID            string            `json:"id"`
				PaymentMethod string            `json:"payment_method"`
				Customer      string            `json:"customer"`
				Metadata      map[string]string `json:"metadata"`
			} `json:"object"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return fmt.Errorf("setup_intent.succeeded: parse payload: %w", err)
	}

	customerID := parsed.Data.Object.Metadata["velox_customer_id"]
	pmID := parsed.Data.Object.PaymentMethod
	if customerID == "" || pmID == "" {
		slog.Warn("setup_intent.succeeded missing velox_customer_id or payment_method — skipping",
			"stripe_event_id", event.StripeEventID)
		return nil
	}

	if err := s.pmAttacher.AttachForWebhook(ctx, tenantID, customerID, pmID); err != nil {
		return fmt.Errorf("attach payment method: %w", err)
	}

	slog.Info("payment method attached via setup_intent",
		"stripe_event_id", event.StripeEventID,
		"customer_id", customerID,
		"stripe_pm_id", pmID,
	)

	s.fireEvent(ctx, tenantID, "payment_method.attached", map[string]any{
		"customer_id":              customerID,
		"stripe_payment_method_id": pmID,
	})

	return nil
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

	// Bind effective-now from the invoice so paid_at lands in
	// simulated time on clock-pinned invoices. Stripe's webhook fires
	// in wall-clock 2026 even when the invoice belongs to a clock
	// frozen at 2024-04 — without binding, paid_at would leak
	// wall-clock and the dashboard would show "Paid on 2026-05-08"
	// for a simulation-2024 invoice.
	ctx = s.bindForInvoice(ctx, tenantID, inv.ID)
	now := clock.Now(ctx)

	// Single atomic operation: mark paid, zero amount_due, record PI + paid_at
	if _, err := s.invoices.MarkPaid(ctx, tenantID, inv.ID, event.PaymentIntentID, now); err != nil {
		return fmt.Errorf("mark invoice paid: %w", err)
	}

	slog.Info("payment succeeded",
		"invoice_id", inv.ID,
		"payment_intent_id", event.PaymentIntentID,
	)

	// Stamp the card actually charged onto the invoice so the
	// activity timeline can show "Invoice paid · via Visa •••• 4242"
	// (ADR-020). Best-effort — a missing CardFetcher, a non-card
	// PM, or a transient Stripe API error all fall through to
	// "Invoice paid · $29.00" with no sub-line. Lookup goes
	// directly through Stripe (not our paymentmethods table) so
	// one-off Checkout cards the customer never saved still show.
	if s.cardFetcher != nil {
		card, cardErr := s.cardFetcher.FetchCardForPaymentIntent(ctx, event.PaymentIntentID)
		if cardErr != nil {
			slog.Warn("payment succeeded: card resolve failed (timeline sub-line will be empty)",
				"invoice_id", inv.ID, "payment_intent_id", event.PaymentIntentID, "error", cardErr)
		} else if card.Brand != "" || card.Last4 != "" {
			if err := s.invoices.SetPaymentCard(ctx, tenantID, inv.ID, card.Brand, card.Last4); err != nil {
				slog.Warn("payment succeeded: persist card details failed",
					"invoice_id", inv.ID, "error", err)
			}
		}
	}

	s.fireEvent(ctx, tenantID, domain.EventPaymentSucceeded, map[string]any{
		"invoice_id":        inv.ID,
		"customer_id":       inv.CustomerID,
		"payment_intent_id": event.PaymentIntentID,
		"amount_cents":      inv.TotalAmountCents,
		"currency":          inv.Currency,
	})

	// Send payment receipt email asynchronously. The goroutine
	// outlives the webhook request, so the request ctx would be
	// canceled by the time GetCustomerEmail / SendPaymentReceipt
	// runs — observed in prod logs as "context canceled" warnings.
	// Build a detached ctx with a 30s timeout, pinning the tenant
	// + mode the request was operating in (per the ctx-attribute
	// audit pattern). Captures everything the downstream
	// code reads off ctx without inheriting the request lifecycle.
	if s.emailReceipt != nil && s.customerEmail != nil {
		livemode := postgres.Livemode(ctx)
		go func() {
			workerCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			workerCtx = veloxauth.WithTenantID(workerCtx, tenantID)
			workerCtx = postgres.WithLivemode(workerCtx, livemode)

			email, name, err := s.customerEmail.GetCustomerEmail(workerCtx, tenantID, inv.CustomerID)
			if err != nil || email == "" {
				slog.Warn("skip payment receipt email — cannot resolve customer email",
					"invoice_id", inv.ID, "customer_id", inv.CustomerID, "error", err)
				return
			}
			if err := s.emailReceipt.SendPaymentReceipt(workerCtx, tenantID, email, name, inv.InvoiceNumber, inv.TotalAmountCents, inv.Currency, inv.PublicToken); err != nil {
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

	// Bind effective-now so dunning's StartDunning (called below) and
	// any UpdatePayment-side stamps land in simulated time on
	// clock-pinned invoices.
	ctx = s.bindForInvoice(ctx, tenantID, inv.ID)

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

	// Auto-start dunning for failed payments.
	//
	// failureAt is the simulated cycle-close instant — the moment in the
	// invoice's own time domain when this charge "should" have happened.
	// For clock-pinned invoices under catchup, frozen_time (= advance-end)
	// is days or months after the actual cycle close; anchoring dunning's
	// next_action_at there pushes the first retry past advance-end and
	// the operator's Advance click fires zero retries.
	//
	// Derive failureAt as the latest invoice period boundary at or before
	// IssuedAt. For in_arrears the cycle fires at BillingPeriodEnd (e.g.
	// May 1 for an Apr 1–May 1 elapsed period). For in_advance it fires
	// at BillingPeriodStart (e.g. May 1 for a May 1–May 31 upcoming
	// period). Picking the latest boundary ≤ IssuedAt gives the right
	// answer in both directions. For manual invoices with no period
	// fields, falls back to IssuedAt or the current clock — both
	// wall-clock-equivalent in production.
	if s.dunning != nil {
		failureAt := simulatedFailureAt(inv)
		if _, err := s.dunning.StartDunning(ctx, tenantID, inv.ID, inv.CustomerID, failureAt); err != nil {
			slog.Warn("failed to start dunning",
				"invoice_id", inv.ID,
				"error", err,
			)
			// Non-fatal: invoice is already marked failed, dunning can be started manually
		} else {
			slog.Info("dunning started for failed payment", "invoice_id", inv.ID)
		}
	}

	// Suppress the customer-facing email when the decline came from an
	// interactive Pay flow (customer is on the hosted invoice page and
	// already saw "Your card was declined" inline). Sending an email
	// telling them what they just saw is noise. Auto-charge declines
	// (no velox_purpose, or velox_purpose != hosted_invoice_pay)
	// still send — the customer wasn't watching.
	purpose := piPurposeFromPayload(event.Payload)
	if purpose == "hosted_invoice_pay" {
		slog.Info("skip post-decline email — interactive Pay flow",
			"invoice_id", inv.ID, "payment_intent_id", event.PaymentIntentID)
		return nil
	}

	// Send the payment-failed email. Points the customer at the hosted
	// invoice page (long-lived public_token), where they can update PM
	// and retry. Same template as dunning retries — this is the
	// "first attempt failed" notification, dunning sends subsequent
	// retry warnings on its own schedule.
	if s.emailPaymentFailed != nil {
		// Detached ctx — the webhook request returns before this
		// goroutine completes, so request ctx would cancel mid-call.
		livemode := postgres.Livemode(ctx)
		go func() {
			workerCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			workerCtx = veloxauth.WithTenantID(workerCtx, tenantID)
			workerCtx = postgres.WithLivemode(workerCtx, livemode)

			if s.customerEmail == nil {
				slog.Error("payment failed email — customer email resolver not wired",
					"invoice_id", inv.ID)
				return
			}
			email, name, err := s.customerEmail.GetCustomerEmail(workerCtx, tenantID, inv.CustomerID)
			if err != nil || email == "" {
				slog.Warn("skip payment failed email — cannot resolve customer email",
					"invoice_id", inv.ID, "customer_id", inv.CustomerID, "error", err)
				return
			}
			if err := s.emailPaymentFailed.SendPaymentFailed(workerCtx, tenantID, email, name, inv.InvoiceNumber, failureMsg, inv.PublicToken); err != nil {
				slog.Error("failed to send payment failed email",
					"invoice_id", inv.ID, "email", email, "error", err)
			}
		}()
	}

	return nil
}

// piPurposeFromPayload extracts data.object.metadata.velox_purpose from
// a Stripe webhook event payload. Returns "" if not present. The
// payload["raw"] field is the JSON body of the Stripe event; we keep
// the parse narrow to just the field we need so unrelated schema
// drift doesn't break extraction.
func piPurposeFromPayload(payload map[string]any) string {
	raw, ok := payload["raw"]
	if !ok {
		return ""
	}
	rawStr, ok := raw.(string)
	if !ok {
		return ""
	}
	var parsed struct {
		Data struct {
			Object struct {
				Metadata map[string]string `json:"metadata"`
			} `json:"object"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(rawStr), &parsed); err != nil {
		return ""
	}
	return parsed.Data.Object.Metadata["velox_purpose"]
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

	// A checkout.session.completed event fires for BOTH save-PM and
	// no-save-PM flows. We only want to mark the customer's setup
	// as 'ready' when a PM was actually attached to the Stripe
	// customer (= can be auto-charged for future invoices).
	// FetchCardDetails lists PMs attached to the customer; success
	// confirms attachment, failure means no PM saved (a one-off
	// hosted-invoice payment without ticking "save card").
	//
	// Pre-fix the upsert ran unconditionally with status='ready'
	// + empty card details on no-save flows — operator-reported
	// inconsistency: dashboard showed "Payment method active" with
	// no card on file. Now no-save flows leave the setup row at
	// its prior state.
	if s.cardFetcher == nil || event.CustomerExternalID == "" {
		slog.Info("checkout.session.completed: no cardFetcher or stripe customer — skipping setup update",
			"customer_id", customerID)
		return nil
	}
	card, err := s.cardFetcher.FetchCardDetails(ctx, event.CustomerExternalID)
	if err != nil {
		// No PM attached to the Stripe customer — operator chose
		// not to save. The PI on this checkout session still
		// succeeded (payment_intent.succeeded handles invoice
		// state); we just don't update the customer's PM record.
		slog.Info("checkout.session.completed: no saved PM (one-off payment) — leaving customer setup unchanged",
			"customer_id", customerID,
			"stripe_customer_id", event.CustomerExternalID,
			"reason", err.Error())
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
		CardBrand:                   card.Brand,
		CardLast4:                   card.Last4,
		CardExpMonth:                card.ExpMonth,
		CardExpYear:                 card.ExpYear,
		StripePaymentMethodID:       card.PaymentMethodID,
	}

	if _, err := s.paymentSetups.UpsertPaymentSetup(ctx, tenantID, setup); err != nil {
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
