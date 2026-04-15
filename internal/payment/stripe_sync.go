package payment

import (
	"context"
	"fmt"

	"github.com/stripe/stripe-go/v82"
	stripecustomer "github.com/stripe/stripe-go/v82/customer"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// StripeBillingSync syncs billing profile data to Stripe customer objects.
type StripeBillingSync struct {
	apiKey string
}

func NewStripeBillingSync(apiKey string) *StripeBillingSync {
	return &StripeBillingSync{apiKey: apiKey}
}

func (s *StripeBillingSync) SyncBillingProfile(_ context.Context, stripeCustomerID string, bp domain.CustomerBillingProfile) error {
	stripe.Key = s.apiKey
	params := &stripe.CustomerParams{}

	// Name: prefer legal name, fall back to email
	if bp.LegalName != "" {
		params.Name = stripe.String(bp.LegalName)
	}
	if bp.Email != "" {
		params.Email = stripe.String(bp.Email)
	}
	if bp.Phone != "" {
		params.Phone = stripe.String(bp.Phone)
	}

	// Address
	if bp.AddressLine1 != "" || bp.Country != "" {
		params.Address = &stripe.AddressParams{
			Line1:      stripe.String(bp.AddressLine1),
			Line2:      stripe.String(bp.AddressLine2),
			City:       stripe.String(bp.City),
			State:      stripe.String(bp.State),
			PostalCode: stripe.String(bp.PostalCode),
			Country:    stripe.String(bp.Country),
		}
	}

	_, err := stripecustomer.Update(stripeCustomerID, params)
	if err != nil {
		return fmt.Errorf("stripe customer update: %w", err)
	}
	return nil
}
