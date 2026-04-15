package payment

import (
	"context"
	"fmt"

	"github.com/stripe/stripe-go/v82"
	"github.com/stripe/stripe-go/v82/refund"
)

// StripeRefunder processes refunds via the Stripe API.
type StripeRefunder struct {
	apiKey string
}

func NewStripeRefunder(apiKey string) *StripeRefunder {
	if apiKey == "" {
		return nil
	}
	return &StripeRefunder{apiKey: apiKey}
}

func (r *StripeRefunder) CreateRefund(_ context.Context, paymentIntentID string, amountCents int64) (string, error) {
	stripe.Key = r.apiKey

	ref, err := refund.New(&stripe.RefundParams{
		PaymentIntent: stripe.String(paymentIntentID),
		Amount:        stripe.Int64(amountCents),
	})
	if err != nil {
		return "", fmt.Errorf("stripe refund: %s", stripeErrorMessage(err))
	}

	return ref.ID, nil
}
