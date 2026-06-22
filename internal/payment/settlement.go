package payment

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/platform/clock"
)

// SettlementSource tags which entry point discovered a payment's terminal
// status, for logs/metrics. The settlement side-effects are identical
// regardless of source — that is the whole point of the primitive (ADR-049):
// a dropped-webhook recovery is byte-identical to the webhook it replaces.
type SettlementSource string

const (
	SourceWebhook        SettlementSource = "webhook"         // inbound payment_intent.* event
	SourceReconciler     SettlementSource = "reconciler"      // GetPaymentIntent backstop sweep (Phase 2)
	SourceChargeResponse SettlementSource = "charge_response" // synchronous Confirm:true create response (Phase 3)
	SourceManual         SettlementSource = "manual"          // operator on-demand "Check provider" (Phase 4)
)

// SettleSucceeded transitions an invoice to PAID and fires the complete
// success side-effect set: bind sim-time so paid_at lands on a clock-pinned
// invoice's timeline, MarkPaid (zero amount_due, record PI + paid_at), stamp
// the charged card for the activity timeline, fire payment.succeeded, and
// enqueue the receipt email.
//
// Idempotent and safe to call from any entry point (ADR-049 discover-then-
// settle): MarkPaid is a no-op on an already-paid invoice, and the card-stamp /
// event / receipt steps are best-effort (log-only) so a duplicate call — e.g.
// the webhook arriving after the reconciler already settled — does not fail.
//
// This is the consolidated implementation of what handlePaymentSucceeded did
// inline (ADR-049 Phase 1); the webhook handler now resolves the invoice and
// delegates here.
func (s *Stripe) SettleSucceeded(ctx context.Context, tenantID string, inv domain.Invoice, paymentIntentID string, source SettlementSource) error {
	// Idempotency guard, symmetric with SettleFailed's out-of-order guard:
	// skip if the invoice is already settled paid. The webhook path resolves a
	// `processing` invoice (guard passes), but a non-webhook source — the
	// reconciler recovering a success the webhook already delivered — would
	// otherwise re-fire the receipt email + payment.succeeded event. MarkPaid
	// itself is a no-op on a paid invoice; this guard additionally suppresses
	// the duplicate side-effects.
	if inv.Status == domain.InvoicePaid || inv.PaymentStatus == domain.PaymentSucceeded {
		slog.Info("payment already settled; skipping duplicate success settlement",
			"invoice_id", inv.ID,
			"payment_intent_id", paymentIntentID,
			"source", source,
		)
		return nil
	}

	// Bind effective-now from the invoice so paid_at lands in simulated time
	// on clock-pinned invoices. Stripe's webhook fires in wall-clock 2026 even
	// when the invoice belongs to a clock frozen at 2024-04 — without binding,
	// paid_at would leak wall-clock and the dashboard would show "Paid on
	// 2026-05-08" for a simulation-2024 invoice.
	ctx = s.bindForInvoice(ctx, tenantID, inv.ID)
	now := clock.Now(ctx)

	// Single atomic operation: mark paid, zero amount_due, record PI + paid_at,
	// AND enqueue invoice.paid + payment.succeeded in the SAME tx (the card path).
	// transitioned reports whether THIS call did the finalized→paid move.
	// The line-47 guard is a fast path that catches the SERIAL redelivery
	// (re-read sees paid); a truly CONCURRENT redelivery of the same charge
	// slips past it because both readers saw `processing`. The
	// SELECT … FOR UPDATE serializes those two, and exactly one gets
	// transitioned=true — the once-only gate for both the in-tx events and the
	// post-commit best-effort side-effects below (receipt email, card stamp).
	_, transitioned, err := s.invoices.MarkPaidCardSettlementTransition(ctx, tenantID, inv.ID, paymentIntentID, now)
	if err != nil {
		return fmt.Errorf("mark invoice paid: %w", err)
	}
	if !transitioned {
		slog.Info("payment already settled by a concurrent settler; skipping duplicate side-effects",
			"invoice_id", inv.ID, "payment_intent_id", paymentIntentID, "source", source)
		return nil
	}

	slog.Info("payment succeeded",
		"invoice_id", inv.ID,
		"payment_intent_id", paymentIntentID,
		"source", source,
	)

	// DURABILITY TIERING (2026-06-22 fix). The consistency-critical events —
	// invoice.paid AND payment.succeeded — are now BOTH enqueued INSIDE
	// MarkPaidCardSettlementTransition's tx (invoice/postgres.go), so they are
	// crash-safe and exactly-once with the paid-flip. A crash here can no longer
	// lose payment.succeeded — the only event carrying the Stripe
	// payment_intent_id — which was the gap this fix closed (transactional outbox,
	// ADR-040; the standard pattern: persist the event in the same tx as the state
	// change, the dispatcher delivers it at-least-once afterward).
	//
	// What remains below is post-commit + best-effort BY DESIGN, not oversight —
	// each is the correct contract for its kind, not a consistency-critical event:
	//   - receipt email: an at-least-once human notification with its own
	//     dispatcher retry + 72h/15-attempt DLQ once enqueued. Strict atomicity is
	//     the wrong contract for email, and making it in-tx would drag customer-
	//     email resolution + the suppression-list read under the invoice row lock.
	//     A crash in this sub-ms window can drop the receipt ENQUEUE; that is the
	//     accepted residual. DEFERRED UPGRADE (if a design partner needs guaranteed
	//     receipts): a receipt-pending marker + reconciler re-fire — tracked in
	//     docs/adr/README.md "Open follow-ups".
	//   - card stamp: cosmetic (a timeline sub-line) and a Stripe NETWORK call that
	//     must never be held under the row lock.
	// SettleFailed's symmetric move (payment.failed + failed-email in-tx) is a
	// separate follow-up; its dunning-start stays post-commit (already idempotent
	// via its own UNIQUE-per-invoice constraint, migration 0085).

	// Stamp the card actually charged onto the invoice so the activity
	// timeline can show "Invoice paid · via Visa •••• 4242" (ADR-020).
	// Best-effort — a missing CardFetcher, a non-card PM, or a transient
	// Stripe API error all fall through to "Invoice paid · $29.00" with no
	// sub-line. Lookup goes directly through Stripe (not our paymentmethods
	// table) so one-off Checkout cards the customer never saved still show.
	if s.cardFetcher != nil && paymentIntentID != "" {
		card, cardErr := s.cardFetcher.FetchCardForPaymentIntent(ctx, paymentIntentID)
		if cardErr != nil {
			slog.Warn("payment succeeded: card resolve failed (timeline sub-line will be empty)",
				"invoice_id", inv.ID, "payment_intent_id", paymentIntentID, "error", cardErr)
		} else if card.Brand != "" || card.Last4 != "" {
			if err := s.invoices.SetPaymentCard(ctx, tenantID, inv.ID, card.Brand, card.Last4); err != nil {
				slog.Warn("payment succeeded: persist card details failed",
					"invoice_id", inv.ID, "error", err)
			}
		}
	}

	// payment.succeeded is now enqueued IN-TX by MarkPaidCardSettlementTransition
	// (above), so it commits atomically with the paid-flip. Do NOT also fire it
	// here — that would double-fire it (one in-tx, one post-commit).

	// Enqueue payment receipt email. s.emailReceipt is *OutboxSender
	// (ADR-040), so SendPaymentReceipt is a fast DB INSERT and the
	// dispatcher's retry loop owns delivery + backoff. Failure here logs but
	// does not fail the caller — the payment already committed, and returning
	// an error would make the webhook re-fire the whole event (re-MarkPaid +
	// double-firing the customer-facing event).
	if s.emailReceipt != nil && s.customerEmail != nil {
		email, name, err := s.customerEmail.GetCustomerEmail(ctx, tenantID, inv.CustomerID)
		if err != nil || email == "" {
			slog.Warn("skip payment receipt email — cannot resolve customer email",
				"invoice_id", inv.ID, "customer_id", inv.CustomerID, "error", err)
		} else if err := s.emailReceipt.SendPaymentReceipt(ctx, tenantID, email, name, inv.InvoiceNumber, inv.TotalAmountCents, inv.Currency, inv.PublicToken); err != nil {
			slog.Error("failed to enqueue payment receipt email",
				"invoice_id", inv.ID, "email", email, "error", err)
		}
	}

	return nil
}

// SettleFailed transitions an invoice to FAILED and fires the complete failure
// side-effect set: an out-of-order guard, UpdatePayment(failed), fire
// payment.failed, auto-start dunning (anchored on simulated cycle-close time),
// and enqueue the payment-failed email unless suppressed.
//
// suppressCustomerEmail is the caller's decision to skip the customer-facing
// email: the webhook suppresses it for interactive Pay flows (the customer
// already saw the inline decline) and for dunning-retry PIs (dunning sends its
// own per-attempt notification). The event + dunning fire regardless.
//
// This is the consolidated implementation of what handlePaymentFailed did
// inline (ADR-049 Phase 1); the webhook handler now resolves the invoice,
// computes suppressCustomerEmail from the PI purpose, and delegates here.
func (s *Stripe) SettleFailed(ctx context.Context, tenantID string, inv domain.Invoice, paymentIntentID, failureMsg string, suppressCustomerEmail bool, source SettlementSource) error {
	// Ignore an out-of-order failure for an already-settled invoice. Webhooks
	// arrive at-least-once and without ordering guarantees, so a stale
	// payment_failed can land AFTER the invoice was marked paid (a retried PI
	// failed upstream but a different PI succeeded first, or the success signal
	// simply arrived first). Applying it would flip payment_status back to
	// 'failed', null paid_at, relink the stale PI, and kick off dunning on a
	// paid invoice. Treat it as a no-op. Living in the primitive means every
	// settler (webhook, reconciler, …) inherits the guard.
	if inv.Status == domain.InvoicePaid || inv.PaymentStatus == domain.PaymentSucceeded {
		slog.Info("ignoring out-of-order payment failure for already-settled invoice",
			"invoice_id", inv.ID,
			"invoice_status", inv.Status,
			"payment_status", inv.PaymentStatus,
			"payment_intent_id", paymentIntentID,
			"source", source,
		)
		return nil
	}

	// Bind effective-now so dunning's StartDunning and any UpdatePayment-side
	// stamps land in simulated time on clock-pinned invoices.
	ctx = s.bindForInvoice(ctx, tenantID, inv.ID)

	if failureMsg == "" {
		failureMsg = "payment failed"
	}

	// Record the failure and learn whether THIS delivery is the first to fire
	// the failure-notification set (payment.failed event + customer email +
	// dunning) for this PaymentIntent. Stripe delivers at-least-once and the
	// inbound dedup is a non-atomic read pre-check (HandleWebhook), so two
	// concurrent deliveries of the SAME payment_intent.payment_failed — or a
	// reconciler recovery racing the original webhook — can both reach here.
	// The FOR UPDATE in MarkPaymentFailedReportingTransition serializes them;
	// only the first gets firstForThisPI=true. The duplicate returns false and
	// skips the side-effects below, so the customer isn't emailed twice and
	// integrators don't get two payment.failed events.
	//
	// This is the failed-path twin of SettleSucceeded's transition gate. It is
	// PI-keyed rather than status-keyed because (a) failure is non-terminal —
	// an invoice legitimately re-fails once per dunning retry, each a new PI,
	// and each is a real event that SHOULD notify; and (b) the synchronous
	// charge path stamps payment_status='failed' (same PI) WITHOUT notifying,
	// so a status gate would suppress the webhook's notifications entirely.
	_, firstForThisPI, err := s.invoices.MarkPaymentFailedReportingTransition(ctx, tenantID, inv.ID, paymentIntentID, failureMsg)
	if err != nil {
		return fmt.Errorf("update payment status: %w", err)
	}
	if !firstForThisPI {
		slog.Info("duplicate or out-of-order payment failure for this payment intent; skipping duplicate side-effects",
			"invoice_id", inv.ID, "payment_intent_id", paymentIntentID, "source", source)
		return nil
	}

	slog.Info("payment failed",
		"invoice_id", inv.ID,
		"payment_intent_id", paymentIntentID,
		"failure_message", failureMsg,
		"source", source,
	)

	s.fireEvent(ctx, tenantID, domain.EventPaymentFailed, map[string]any{
		"invoice_id":        inv.ID,
		"customer_id":       inv.CustomerID,
		"payment_intent_id": paymentIntentID,
		"failure_message":   failureMsg,
		"amount_cents":      inv.TotalAmountCents,
		"currency":          inv.Currency,
	})

	// Auto-start dunning for failed payments. failureAt is the simulated
	// cycle-close instant — the moment in the invoice's own time domain when
	// this charge "should" have happened — so dunning's next_action_at lands
	// inside the operator's Advance window for clock-pinned invoices. See
	// simulatedFailureAt.
	//
	// KNOWN GAP (deferred — docs/adr/README.md "Open follow-ups"): this runs
	// POST-COMMIT, behind the firstForThisPI gate that MarkPaymentFailedReporting
	// Transition already committed above. So a crash here (or a transient
	// StartDunning error) is NOT recovered: a same-PI redelivery returns
	// firstForThisPI=false and skips this, and the reconciler only sweeps
	// unknown/processing, never 'failed'. The 0085 UNIQUE guards against DOUBLE-
	// start, NOT never-start — so dunning genuinely won't auto-start in that
	// window (operator can still start it from the attention banner). The fix is
	// a reconciler sweep for 'failed' invoices with no dunning run.
	if s.dunning != nil {
		failureAt := simulatedFailureAt(inv)
		if err := startDunningWithRetry(ctx, s.dunning, tenantID, inv.ID, inv.CustomerID, failureAt); err != nil {
			slog.Error("payment failure StartDunning failed after retries — dunning will NOT auto-retry; operator must start manually from invoice attention banner",
				"invoice_id", inv.ID, "customer_id", inv.CustomerID, "error", err)
		} else {
			slog.Info("dunning started for failed payment", "invoice_id", inv.ID)
		}
	}

	if suppressCustomerEmail {
		return nil
	}

	// Enqueue the payment-failed email. As with the receipt path, this is a
	// fast outbox INSERT and the dispatcher owns delivery retry; failure logs
	// but does not fail the caller (invoice state already committed).
	if s.emailPaymentFailed != nil {
		if s.customerEmail == nil {
			slog.Error("payment failed email — customer email resolver not wired",
				"invoice_id", inv.ID)
		} else if email, name, err := s.customerEmail.GetCustomerEmail(ctx, tenantID, inv.CustomerID); err != nil || email == "" {
			slog.Warn("skip payment failed email — cannot resolve customer email",
				"invoice_id", inv.ID, "customer_id", inv.CustomerID, "error", err)
		} else if err := s.emailPaymentFailed.SendPaymentFailed(ctx, tenantID, email, name, inv.InvoiceNumber, failureMsg, inv.PublicToken); err != nil {
			slog.Error("failed to enqueue payment failed email",
				"invoice_id", inv.ID, "email", email, "error", err)
		}
	}

	return nil
}
