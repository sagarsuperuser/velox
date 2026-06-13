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
	// Purpose is the velox_purpose PI metadata (e.g. "dunning_retry",
	// "hosted_invoice_pay"). Populated by GetPaymentIntent so the reconciler
	// can replicate the webhook's customer-email suppression when it recovers a
	// failure (ADR-049 Phase 2). Empty for the create response / when unset.
	Purpose string
}

// InvoiceUpdater updates invoice payment status.
type InvoiceUpdater interface {
	UpdatePayment(ctx context.Context, tenantID, id string, paymentStatus domain.InvoicePaymentStatus, stripePaymentIntentID, lastPaymentError string, paidAt *time.Time) (domain.Invoice, error)
	UpdateStatus(ctx context.Context, tenantID, id string, status domain.InvoiceStatus) (domain.Invoice, error)
	MarkPaid(ctx context.Context, tenantID, id string, stripePaymentIntentID string, paidAt time.Time) (domain.Invoice, error)
	// MarkPaidReportingTransition is MarkPaid plus a `transitioned` flag —
	// true only when THIS call moved the invoice into paid (false on the
	// already-paid no-op). SettleSucceeded gates its post-paid side-effects
	// on it so a concurrent redelivery of the same charge doesn't double-
	// fire the receipt email / payment.succeeded event.
	MarkPaidReportingTransition(ctx context.Context, tenantID, id string, stripePaymentIntentID string, paidAt time.Time) (domain.Invoice, bool, error)
	// SetPaymentCard stamps the card brand + last4 used to settle
	// an invoice. Optional — empty values render no sub-line in
	// the timeline. Called by handlePaymentSucceeded after MarkPaid
	// lands. ADR-020.
	SetPaymentCard(ctx context.Context, tenantID, id, brand, last4 string) error
	GetByStripePaymentIntentID(ctx context.Context, tenantID, stripePaymentIntentID string) (domain.Invoice, error)
	Get(ctx context.Context, tenantID, id string) (domain.Invoice, error)
}

// WebhookStore persists and queries Stripe webhook events.
//
// Dedup is split across two calls so the dedup marker can be written
// AFTER the business side-effect commits, not before. WasProcessed is
// the read-only pre-check; IngestEvent is the write that both records
// the audit row and claims the dedup slot (ON CONFLICT DO NOTHING).
// HandleWebhook calls IngestEvent only once processing has succeeded
// (or failed permanently) — a transient processing failure persists no
// row, so Stripe's redelivery re-runs the handler instead of being
// short-circuited as a duplicate.
type WebhookStore interface {
	// WasProcessed reports whether an event with this stripe_event_id has
	// already been ingested for the tenant+livemode. Read-only; used as the
	// idempotency pre-check before running side-effects.
	WasProcessed(ctx context.Context, tenantID, stripeEventID string) (bool, error)
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
	return s.chargeInvoice(ctx, tenantID, inv, stripeCustomerID, "")
}

// ChargeInvoiceForDunningRetry charges an invoice as part of a dunning
// retry attempt. Identical to ChargeInvoice except the resulting
// PaymentIntent carries velox_purpose=dunning_retry metadata, which
// the payment_intent.payment_failed webhook handler reads to suppress
// the duplicate generic payment-failed email (dunning sends its own
// per-attempt warning / escalation inline from processRun / exhaustRun).
//
// Exposed as a separate method rather than a variadic option so the
// engine's InvoiceCharger interface stays narrow (default-mode only)
// and the cross-domain import that a shared options type would force
// (billing → payment) is avoided per the per-domain-isolation rule
// in CLAUDE.md.
func (s *Stripe) ChargeInvoiceForDunningRetry(ctx context.Context, tenantID string, inv domain.Invoice, stripeCustomerID string) (domain.Invoice, error) {
	return s.chargeInvoice(ctx, tenantID, inv, stripeCustomerID, piPurposeDunningRetry)
}

func (s *Stripe) chargeInvoice(ctx context.Context, tenantID string, inv domain.Invoice, stripeCustomerID, purpose string) (domain.Invoice, error) {
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
	if purpose != "" {
		metadata["velox_purpose"] = purpose
	}
	// Per-attempt idempotency key. inv.UpdatedAt advances every time
	// UpdatePayment runs (which happens on every prior failed attempt
	// recording last_payment_error), so each genuine retry gets a
	// fresh key — Stripe creates a new PI rather than returning the
	// cached failed one. Within a single attempt, two concurrent
	// callers (scheduler + operator click) read the same UpdatedAt
	// and dedupe correctly: Stripe returns the same PI to both, no
	// double-charge.
	//
	// The `purpose` suffix prevents key collisions between the initial
	// finalize-time auto-charge (purpose="") and the first dunning
	// retry (purpose="dunning_retry"). Without it, both calls use
	// inv.UpdatedAt = T1 (the dunning retry reads the inv with
	// updated_at still pinned to the initial-charge-fail moment
	// because no UpdatePayment runs between them), but the metadata
	// differs because the retry path adds velox_purpose=dunning_retry.
	// Stripe sees "same key, different parameters" → 409. Surfaced
	// 2026-05-19 from a clock-pinned dunning workflow; the bug exists
	// on wall-clock too in theory but is masked because invoice
	// metadata typically doesn't change between consecutive
	// chargeInvoice/ChargeInvoiceForDunningRetry calls in production
	// (they sit far enough apart in time for UpdatedAt to drift via
	// other writes).
	idempotencyKey := fmt.Sprintf("velox_inv_%s_%d", inv.ID, inv.UpdatedAt.UnixNano())
	if purpose != "" {
		idempotencyKey += "_" + purpose
	}
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
			if derr := startDunningWithRetry(ctx, s.dunning, tenantID, inv.ID, inv.CustomerID, failureAt); derr != nil {
				slog.Error("inline StartDunning after known-failed charge — dunning will NOT auto-retry; operator must start manually from invoice attention banner",
					"invoice_id", inv.ID, "customer_id", inv.CustomerID, "error", derr)
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

	// Honor the synchronous confirmation outcome (ADR-049 Phase 3,
	// discover-then-settle). With Confirm:true + OffSession:true, Stripe
	// returns the terminal status in the create RESPONSE for cards — a
	// `succeeded` PI is authoritative WITHOUT waiting for the webhook. Settle
	// inline so the invoice resolves in-request — and, on a test clock, inside
	// the operator's Advance on the simulated timeline — instead of sitting in
	// `processing` until a wall-clock webhook that may never arrive in local
	// dev. The webhook + reconciler stay idempotent backstops (SettleSucceeded
	// skips an already-paid invoice). Genuinely-async statuses (processing /
	// requires_action / requires_confirmation / requires_capture — delayed
	// methods, or rare off-session SCA) stay `processing` and await the webhook.
	if result.Status == "succeeded" {
		if serr := s.SettleSucceeded(ctx, tenantID, inv, result.ID, SourceChargeResponse); serr != nil {
			return domain.Invoice{}, fmt.Errorf("settle succeeded inline: %w", serr)
		}
		// Return the freshly-settled row so callers (dunning retrier, portal,
		// operator charge-now) observe the paid state immediately. On a re-read
		// error the settle already committed — return a patched copy rather
		// than a misleading error.
		settled, gerr := s.invoices.Get(ctx, tenantID, inv.ID)
		if gerr != nil {
			inv.PaymentStatus = domain.PaymentSucceeded
			inv.Status = domain.InvoicePaid
			inv.StripePaymentIntentID = result.ID
			return inv, nil
		}
		return settled, nil
	}

	// Still in flight — record the PI and set processing; the webhook (and the
	// reconciler backstop) will settle it.
	return s.invoices.UpdatePayment(ctx, tenantID, inv.ID,
		domain.PaymentProcessing, result.ID, "", nil)
}

// piPurposeDunningRetry tags a PaymentIntent created by the dunning
// retrier so the payment_intent.payment_failed webhook can suppress
// its generic payment-failed email — dunning's warning/escalation is
// the canonical notification for retry attempts. Stripe-parity (Smart
// Retries sends one email per attempt, not one webhook-email plus one
// engine-email).
const piPurposeDunningRetry = "dunning_retry"

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
// startDunningWithRetry calls StartDunning with bounded inline retry to
// absorb transient blips (DB connection hiccup, brief contention) without
// silently losing the dunning start. StartDunning is idempotent by
// invoice (migration 0085 UNIQUE on dunning_runs), so the retry is safe
// — a successful first attempt followed by a slow concurrent caller
// resolves to the existing row, not a duplicate.
//
// Three attempts with 100ms / 500ms backoff covers the typical transient
// case. Persistent failure (Stripe outage, DB unavailable for seconds)
// surfaces as the returned error; caller upgrades the slog level to
// ERROR so operators have an alertable signal, and the operator path
// (start dunning manually from the invoice attention banner) stays
// available as the last resort.
//
// 2026-05-30 design-debt audit (Tier 1 #5) replaced two log-and-swallow
// sites here and at the inline charge-failure path with this retry.
func startDunningWithRetry(ctx context.Context, dunning DunningStarter, tenantID, invoiceID, customerID string, failureAt time.Time) error {
	delays := []time.Duration{0, 100 * time.Millisecond, 500 * time.Millisecond}
	var lastErr error
	for i, d := range delays {
		if d > 0 {
			select {
			case <-time.After(d):
			case <-ctx.Done():
				return fmt.Errorf("ctx canceled during StartDunning retry (attempt %d): %w", i+1, ctx.Err())
			}
		}
		if _, err := dunning.StartDunning(ctx, tenantID, invoiceID, customerID, failureAt); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	return fmt.Errorf("StartDunning failed after %d attempts: %w", len(delays), lastErr)
}

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

// HandleWebhook processes a Stripe webhook event and updates the
// corresponding invoice.
//
// Dedup ordering (the fix): the dedup marker is written AFTER the
// business side-effect commits, not before. The pre-2026-06 flow
// committed the dedup row first (IngestEvent's own tx) and then ran the
// handler — so a transient handler failure left a committed dedup row,
// the HTTP layer ack'd 200, and Stripe's redelivery short-circuited as
// a duplicate. The event was silently and permanently dropped.
//
// Now:
//  1. WasProcessed is a read-only idempotency pre-check.
//  2. processEvent runs the side-effect (MarkPaid / dunning / etc.).
//  3. On a TRANSIENT failure we persist NO row and return the error —
//     the HTTP handler turns that into a 5xx so Stripe redelivers and
//     the handler re-runs. "dedup row exists" now strictly implies
//     "side-effect committed."
//  4. On success OR a PERMANENT failure (the event references an entity
//     that isn't in our DB and never will be — errs.ErrNotFound) we
//     IngestEvent to persist the audit row + claim the dedup slot, then
//     ack. Acking a permanent failure is deliberate: redelivering it
//     forever achieves nothing.
//
// Business side-effects on these paths are idempotent (MarkPaid is a
// no-op on an already-paid invoice; StartDunning is UNIQUE per invoice),
// so a redelivery that races a slow first delivery and double-processes
// converges to the same state — the ON CONFLICT DO NOTHING in
// IngestEvent collapses the duplicate audit row.
func (s *Stripe) HandleWebhook(ctx context.Context, tenantID string, event domain.StripeWebhookEvent) error {
	seen, err := s.webhooks.WasProcessed(ctx, tenantID, event.StripeEventID)
	if err != nil {
		return fmt.Errorf("webhook dedup pre-check: %w", err)
	}
	if seen {
		slog.Info("duplicate webhook event, skipping", "stripe_event_id", event.StripeEventID)
		return nil
	}

	if procErr := s.processEvent(ctx, tenantID, event); procErr != nil {
		if isTransientWebhookError(procErr) {
			// No dedup row written — return the error so the HTTP layer
			// responds 5xx and Stripe redelivers the event.
			return procErr
		}
		// Permanent: record the audit row (and claim the dedup slot) so the
		// event shows up in history with its outcome, then ack.
		slog.Warn("webhook permanently unprocessable — acking without retry",
			"stripe_event_id", event.StripeEventID,
			"event_type", event.EventType,
			"error", procErr)
	}

	// Side-effect committed (or permanently un-processable) — claim the
	// dedup slot + persist the audit row.
	if _, _, err := s.webhooks.IngestEvent(ctx, tenantID, event); err != nil {
		return fmt.Errorf("ingest webhook event: %w", err)
	}
	return nil
}

// processEvent runs the side-effect for one webhook event. Returns nil on
// success (including no-op event types). A non-nil error is classified by
// HandleWebhook as transient (redeliver) or permanent (ack) via
// isTransientWebhookError.
func (s *Stripe) processEvent(ctx context.Context, tenantID string, event domain.StripeWebhookEvent) error {
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

// isTransientWebhookError reports whether a webhook processing error is
// worth a Stripe redelivery. A missing target entity (errs.ErrNotFound)
// is permanent — the event references an invoice/customer that isn't in
// our DB, and redelivering won't conjure it — so we ack it. Everything
// else (DB write failure, contention, a downstream timeout) is treated
// as transient: return it, respond 5xx, let Stripe redeliver.
func isTransientWebhookError(err error) bool {
	return !errors.Is(err, errs.ErrNotFound)
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

	// Webhook is one of the discover-then-settle entry points (ADR-049): it
	// resolves the invoice, then hands the terminal outcome to the shared
	// settlement primitive so the side-effects (mark paid, card stamp,
	// payment.succeeded event, receipt email) are identical to every other
	// settler.
	return s.SettleSucceeded(ctx, tenantID, inv, event.PaymentIntentID, SourceWebhook)
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

	// Suppress the customer-facing email for two flows; the event + dunning
	// still fire (they happen inside the primitive before the email step):
	//   - hosted_invoice_pay: an interactive Pay flow — the customer is on the
	//     hosted invoice page and already saw "Your card was declined" inline;
	//     an email telling them what they just saw is noise.
	//   - dunning_retry: this PI was created by the dunning retrier, which
	//     already sent its own per-attempt warning/escalation email; firing the
	//     generic payment-failed email too would double-notify for the same
	//     attempt (Stripe Smart Retries / Lago shape: one email per attempt).
	purpose := piPurposeFromPayload(event.Payload)
	suppressEmail := purpose == "hosted_invoice_pay" || purpose == "dunning_retry"

	// Discover-then-settle (ADR-049): hand the terminal outcome to the shared
	// primitive, which owns the out-of-order guard, mark-failed, payment.failed
	// event, dunning auto-start, and the (possibly suppressed) failure email.
	return s.SettleFailed(ctx, tenantID, inv, event.PaymentIntentID, event.FailureMessage, suppressEmail, SourceWebhook)
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
