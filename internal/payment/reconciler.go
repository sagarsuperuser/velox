package payment

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/payment/breaker"
)

// ReconcileInvoiceStore is the narrow interface the reconciler needs from
// the invoice store. Kept separate from InvoiceUpdater so the reconciler
// can be wired independently of the hot charge / webhook paths.
type ReconcileInvoiceStore interface {
	ListUnknownPayments(ctx context.Context, olderThan time.Time, limit int) ([]domain.Invoice, error)
	UpdatePayment(ctx context.Context, tenantID, id string, ps domain.InvoicePaymentStatus, stripePaymentIntentID, lastPaymentError string, paidAt *time.Time) (domain.Invoice, error)
	MarkPaid(ctx context.Context, tenantID, id string, stripePaymentIntentID string, paidAt time.Time) (domain.Invoice, error)
}

// Reconciler resolves invoices stuck in PaymentUnknown by asking Stripe for
// the authoritative PaymentIntent state. An invoice lands in PaymentUnknown
// when ChargeInvoice returns an ambiguous error (5xx / timeout / connection
// reset) — Stripe might have processed the charge server-side, so we must
// not blindly retry.
//
// Cool-off window: unknown invoices younger than `olderThan` are skipped to
// let the webhook (payment_intent.succeeded / failed) arrive first, which
// resolves the state without an extra API call.
type Reconciler struct {
	client    StripeClient
	invoices  ReconcileInvoiceStore
	olderThan time.Duration
	now       func() time.Time // injectable for tests
	breaker   *breaker.Breaker // optional; skip tenants whose breaker is open
}

// NewReconciler constructs a Reconciler. If olderThan <= 0, defaults to 60s.
func NewReconciler(client StripeClient, invoices ReconcileInvoiceStore, olderThan time.Duration) *Reconciler {
	if olderThan <= 0 {
		olderThan = 60 * time.Second
	}
	return &Reconciler{
		client:    client,
		invoices:  invoices,
		olderThan: olderThan,
		now:       func() time.Time { return time.Now().UTC() },
	}
}

// SetBreaker wires the same per-tenant circuit breaker used by ChargeInvoice.
// When a tenant's breaker is open, the reconciler skips their unresolved
// invoices this tick rather than piling Stripe reads onto an already-sick
// service; the next tick after cooldown picks them up.
func (r *Reconciler) SetBreaker(b *breaker.Breaker) {
	r.breaker = b
}

// Run scans for unresolved PaymentUnknown invoices older than the cool-off
// window and reconciles each one against Stripe. Returns the number
// resolved (succeeded or failed) and any per-invoice errors.
func (r *Reconciler) Run(ctx context.Context, limit int) (int, []error) {
	before := r.now().Add(-r.olderThan)
	invoices, err := r.invoices.ListUnknownPayments(ctx, before, limit)
	if err != nil {
		return 0, []error{fmt.Errorf("list unknown payments: %w", err)}
	}

	var (
		resolved int
		errs     []error
	)
	for _, inv := range invoices {
		changed, err := r.reconcileOne(ctx, inv)
		if err != nil {
			errs = append(errs, fmt.Errorf("invoice %s: %w", inv.ID, err))
			continue
		}
		if changed {
			resolved++
		}
	}
	return resolved, errs
}

// reconcileOne queries Stripe for the PI and transitions the invoice to
// succeeded / failed / still-unknown based on the returned status.
func (r *Reconciler) reconcileOne(ctx context.Context, inv domain.Invoice) (bool, error) {
	if inv.StripePaymentIntentID == "" {
		// No PI ID was returned with the ambiguous error — we cannot query
		// Stripe. Mark the invoice failed so the operator / dunning flow
		// can decide the next step; a safe subsequent retry will generate
		// a new PI (original idempotency key is unchanged, so Stripe will
		// return the same result if it somehow did succeed — but without
		// the PI ID we cannot discover that here).
		_, err := r.invoices.UpdatePayment(ctx, inv.TenantID, inv.ID,
			domain.PaymentFailed, "", "unknown outcome: no payment_intent_id to reconcile", nil)
		if err != nil {
			return false, fmt.Errorf("mark failed (no pi): %w", err)
		}
		slog.Warn("reconciled unknown → failed (no PI to query)",
			"invoice_id", inv.ID, "tenant_id", inv.TenantID)
		return true, nil
	}

	var res PaymentIntentResult
	var err error
	if r.breaker != nil {
		var out any
		out, err = r.breaker.Execute(ctx, inv.TenantID, func(ctx context.Context) (any, error) {
			return r.client.GetPaymentIntent(ctx, inv.StripePaymentIntentID)
		})
		if errors.Is(err, breaker.ErrOpen) {
			// Breaker open for this tenant — don't pile reads onto Stripe
			// while it's struggling; return silently so Run() doesn't log
			// a per-invoice error for every unresolved invoice.
			return false, nil
		}
		if out != nil {
			res = out.(PaymentIntentResult)
		}
	} else {
		res, err = r.client.GetPaymentIntent(ctx, inv.StripePaymentIntentID)
	}
	if err != nil {
		// Stripe itself returned 5xx on the reconcile query — leave the
		// invoice unknown, try again next tick.
		return false, fmt.Errorf("get payment intent %s: %w", inv.StripePaymentIntentID, err)
	}

	switch res.Status {
	case "succeeded":
		now := r.now()
		if _, err := r.invoices.MarkPaid(ctx, inv.TenantID, inv.ID, res.ID, now); err != nil {
			return false, fmt.Errorf("mark paid: %w", err)
		}
		slog.Info("reconciled unknown → succeeded",
			"invoice_id", inv.ID, "stripe_payment_intent_id", res.ID)
		return true, nil

	case "canceled", "requires_payment_method":
		// Stripe confirms the charge did NOT succeed. Safe to mark failed;
		// the existing handlePaymentFailed flow (dunning, email) runs via
		// webhook in parallel and will be idempotent via webhook dedup.
		if _, err := r.invoices.UpdatePayment(ctx, inv.TenantID, inv.ID,
			domain.PaymentFailed, res.ID, "reconciled: "+res.Status, nil); err != nil {
			return false, fmt.Errorf("mark failed: %w", err)
		}
		slog.Info("reconciled unknown → failed",
			"invoice_id", inv.ID, "stripe_payment_intent_id", res.ID, "stripe_status", res.Status)
		return true, nil

	case "processing", "requires_action", "requires_confirmation", "requires_capture":
		// Still in flight on Stripe's side — give the webhook more time.
		slog.Debug("reconcile: PI still in flight, skipping",
			"invoice_id", inv.ID, "stripe_payment_intent_id", res.ID, "stripe_status", res.Status)
		return false, nil

	default:
		return false, fmt.Errorf("unexpected stripe PI status %q", res.Status)
	}
}
