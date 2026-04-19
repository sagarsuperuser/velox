package payment

import (
	"context"
	"errors"
	"fmt"

	"github.com/stripe/stripe-go/v82"
	stripecustomer "github.com/stripe/stripe-go/v82/customer"
	"github.com/stripe/stripe-go/v82/paymentintent"
	"github.com/stripe/stripe-go/v82/paymentmethod"
)

// stripeErrorMessage extracts a clean human-readable message from Stripe SDK errors.
func stripeErrorMessage(err error) string {
	var stripeErr *stripe.Error
	if errors.As(err, &stripeErr) {
		if stripeErr.Msg != "" {
			return stripeErr.Msg
		}
		return string(stripeErr.Code)
	}
	return err.Error()
}

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
	metadata := make(map[string]string)
	for k, v := range params.Metadata {
		metadata[k] = v
	}

	// Use the customer's default payment method, fall back to most recent card
	cus, err := stripecustomer.Get(params.CustomerID, nil)
	var defaultPM string
	if err == nil && cus.InvoiceSettings != nil && cus.InvoiceSettings.DefaultPaymentMethod != nil {
		defaultPM = cus.InvoiceSettings.DefaultPaymentMethod.ID
	}
	if defaultPM == "" {
		// Fall back to most recently created card
		var latest *stripe.PaymentMethod
		pmIter := paymentmethod.List(&stripe.PaymentMethodListParams{
			Customer: stripe.String(params.CustomerID),
			Type:     stripe.String("card"),
		})
		for pmIter.Next() {
			pm := pmIter.PaymentMethod()
			if latest == nil || pm.Created > latest.Created {
				latest = pm
			}
		}
		if latest != nil {
			defaultPM = latest.ID
		}
	}
	if defaultPM == "" {
		return PaymentIntentResult{}, fmt.Errorf("customer has no payment method on file")
	}

	pi, err := paymentintent.New(&stripe.PaymentIntentParams{
		Amount:        stripe.Int64(params.AmountCents),
		Currency:      stripe.String(params.Currency),
		Customer:      stripe.String(params.CustomerID),
		PaymentMethod: stripe.String(defaultPM),
		Confirm:       stripe.Bool(true),
		OffSession:    stripe.Bool(true),
		Params: stripe.Params{
			IdempotencyKey: stripe.String(fmt.Sprintf("%s_%s", params.IdempotencyKey, defaultPM)),
			Metadata:       metadata,
		},
		Description: stripe.String(params.Description),
	})
	if err != nil {
		return PaymentIntentResult{}, fmt.Errorf("%s", stripeErrorMessage(err))
	}

	return PaymentIntentResult{
		ID:           pi.ID,
		Status:       string(pi.Status),
		ClientSecret: pi.ClientSecret,
	}, nil
}

func (c *LiveStripeClient) FetchCardDetails(_ context.Context, stripeCustomerID string) (CardDetails, error) {
	// Get the most recently created card
	var latest *stripe.PaymentMethod
	pmIter := paymentmethod.List(&stripe.PaymentMethodListParams{
		Customer: stripe.String(stripeCustomerID),
		Type:     stripe.String("card"),
	})
	for pmIter.Next() {
		pm := pmIter.PaymentMethod()
		if latest == nil || pm.Created > latest.Created {
			latest = pm
		}
	}
	if latest == nil || latest.Card == nil {
		return CardDetails{}, fmt.Errorf("no card found for customer %s", stripeCustomerID)
	}

	// Set this card as the customer's default payment method
	_, _ = stripecustomer.Update(stripeCustomerID, &stripe.CustomerParams{
		InvoiceSettings: &stripe.CustomerInvoiceSettingsParams{
			DefaultPaymentMethod: stripe.String(latest.ID),
		},
	})

	return CardDetails{
		PaymentMethodID: latest.ID,
		Brand:           string(latest.Card.Brand),
		Last4:           latest.Card.Last4,
		ExpMonth:        int(latest.Card.ExpMonth),
		ExpYear:         int(latest.Card.ExpYear),
	}, nil
}

func (c *LiveStripeClient) CancelPaymentIntent(_ context.Context, paymentIntentID string) error {
	_, err := paymentintent.Cancel(paymentIntentID, nil)
	if err != nil {
		return fmt.Errorf("stripe cancel: %s", stripeErrorMessage(err))
	}
	return nil
}
