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

func (r *StripeRefunder) CreateRefund(ctx context.Context, paymentIntentID string, amountCents int64) (string, error) {
	sc := r.clients.ForCtx(ctx)
	if sc == nil {
		return "", ErrStripeNotConfigured
	}
	ref, err := sc.Refunds.New(&stripe.RefundParams{
		PaymentIntent: stripe.String(paymentIntentID),
		Amount:        stripe.Int64(amountCents),
	})
	if err != nil {
		return "", fmt.Errorf("stripe refund: %s", stripeErrorMessage(err))
	}

	return ref.ID, nil
}
