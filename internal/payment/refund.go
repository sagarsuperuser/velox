package payment

import (
	"context"
	"fmt"

	"github.com/stripe/stripe-go/v82"
)

// StripeRefunder processes refunds via the Stripe API.
// Mode-aware: selects live/test client per ctx livemode.
type StripeRefunder struct {
	clients *StripeClients
}

func NewStripeRefunder(clients *StripeClients) *StripeRefunder {
	if !clients.Has() {
		return nil
	}
	return &StripeRefunder{clients: clients}
}

// CreateRefund creates a Stripe refund for the given PaymentIntent.
//
// `idempotencyKey` is mandatory in production — same key + same params
// returns the existing refund without creating a duplicate. Velox passes
// `velox_cn_<credit_note_id>` so that a credit-note Issue() retry after
// a partial failure (e.g. DB hiccup on the post-refund credit grant
// step) hits Stripe's cache and gets back the original refund_id,
// not a second refund.
//
// Without this protection, the pre-fix shape was: Stripe refund
// succeeds → in-memory refund_id set → credit grant fails → function
// returns error → CN stays draft → operator retries → Stripe called
// again with no idempotency key → DUPLICATE refund → customer over-
// refunded (caught 2026-05-22).
func (r *StripeRefunder) CreateRefund(ctx context.Context, paymentIntentID string, amountCents int64, idempotencyKey string) (string, error) {
	sc := r.clients.ForCtx(ctx)
	if sc == nil {
		return "", ErrStripeNotConfigured
	}
	params := &stripe.RefundCreateParams{
		PaymentIntent: stripe.String(paymentIntentID),
		Amount:        stripe.Int64(amountCents),
	}
	if idempotencyKey != "" {
		params.IdempotencyKey = stripe.String(idempotencyKey)
	}
	ref, err := sc.V1Refunds.Create(ctx, params)
	if err != nil {
		return "", fmt.Errorf("stripe refund: %s", stripeErrorMessage(err))
	}

	return ref.ID, nil
}
