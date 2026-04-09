package payment

import (
	"context"
	"fmt"

	"github.com/stripe/stripe-go/v82"
	"github.com/stripe/stripe-go/v82/paymentintent"
)

// LiveStripeClient wraps the Stripe SDK for real PaymentIntent operations.
// Implements the StripeClient interface used by the payment adapter.
type LiveStripeClient struct {
	apiKey string
}

// NewLiveStripeClient creates a client with the given Stripe secret key.
// Returns nil if apiKey is empty (allows graceful degradation).
func NewLiveStripeClient(apiKey string) *LiveStripeClient {
	if apiKey == "" {
		return nil
	}
	return &LiveStripeClient{apiKey: apiKey}
}

func (c *LiveStripeClient) CreatePaymentIntent(_ context.Context, params PaymentIntentParams) (PaymentIntentResult, error) {
	stripe.Key = c.apiKey

	metadata := make(map[string]string)
	for k, v := range params.Metadata {
		metadata[k] = v
	}

	pi, err := paymentintent.New(&stripe.PaymentIntentParams{
		Amount:     stripe.Int64(params.AmountCents),
		Currency:   stripe.String(params.Currency),
		Customer:   stripe.String(params.CustomerID),
		Confirm:    stripe.Bool(true),
		OffSession: stripe.Bool(true),
		Params: stripe.Params{
			IdempotencyKey: stripe.String(params.IdempotencyKey),
			Metadata:       metadata,
		},
		Description:           stripe.String(params.Description),
		AutomaticPaymentMethods: &stripe.PaymentIntentAutomaticPaymentMethodsParams{
			Enabled: stripe.Bool(true),
		},
	})
	if err != nil {
		return PaymentIntentResult{}, fmt.Errorf("stripe: %w", err)
	}

	return PaymentIntentResult{
		ID:           pi.ID,
		Status:       string(pi.Status),
		ClientSecret: pi.ClientSecret,
	}, nil
}

func (c *LiveStripeClient) CancelPaymentIntent(_ context.Context, paymentIntentID string) error {
	stripe.Key = c.apiKey

	_, err := paymentintent.Cancel(paymentIntentID, nil)
	if err != nil {
		return fmt.Errorf("stripe cancel: %w", err)
	}
	return nil
}
