// Package paymentmethods is the customer-facing view of payment methods.
//
// payment_methods rows are the canonical many-to-one store (multiple cards
// per customer, one flagged default). The existing customer_payment_setups
// table stays as the 1:1 denorm summary that billing reads for the "one
// default card" path — this package writes both on every mutation.
//
// All routes in this package run under customerportal.Middleware, so the
// caller identity is a customer (not a tenant operator); tenantID +
// customerID are read from ctx and pushed into BeginTx.
package paymentmethods

import "time"

// PaymentMethod mirrors one row in payment_methods.
type PaymentMethod struct {
	ID                    string
	TenantID              string
	Livemode              bool
	CustomerID            string
	StripePaymentMethodID string
	Type                  string // "card" for now; other Stripe PM types later
	CardBrand             string
	CardLast4             string
	CardExpMonth          int
	CardExpYear           int
	IsDefault             bool
	DetachedAt            *time.Time
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

// IsActive — convenience for "attached and usable".
func (p PaymentMethod) IsActive() bool { return p.DetachedAt == nil }
