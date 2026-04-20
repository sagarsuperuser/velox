package payment

import (
	"context"
	"fmt"

	"github.com/stripe/stripe-go/v82"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// StripeBillingSync syncs billing profile data to Stripe customer objects.
// Mode-aware: uses the live or test client based on ctx livemode.
type StripeBillingSync struct {
	clients *StripeClients
}

// NewStripeBillingSync returns nil if clients has no configured modes. The
// caller then skips sync entirely rather than silently no-opping each call.
func NewStripeBillingSync(clients *StripeClients) *StripeBillingSync {
	if !clients.Has() {
		return nil
	}
	return &StripeBillingSync{clients: clients}
}

func (s *StripeBillingSync) SyncBillingProfile(ctx context.Context, stripeCustomerID string, bp domain.CustomerBillingProfile) error {
	sc := s.clients.ForCtx(ctx)
	if sc == nil {
		return ErrStripeNotConfigured
	}

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

	_, err := sc.Customers.Update(stripeCustomerID, params)
	if err != nil {
		return fmt.Errorf("stripe customer update: %w", err)
	}
	return nil
}
