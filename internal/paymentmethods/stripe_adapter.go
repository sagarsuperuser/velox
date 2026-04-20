package paymentmethods

import (
	"context"
	"errors"
	"fmt"

	"github.com/stripe/stripe-go/v82"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/payment"
)

// StripeAdapter wires the payment-methods Service to the existing
// payment.StripeClients pool. Mode-aware — ForCtx(ctx) returns the right
// client for the caller's livemode, set by customerportal.Middleware.
type StripeAdapter struct {
	clients *payment.StripeClients
	summary PaymentSetupSummaryWriter
}

func NewStripeAdapter(clients *payment.StripeClients, summary PaymentSetupSummaryWriter) *StripeAdapter {
	return &StripeAdapter{clients: clients, summary: summary}
}

var _ StripeAPI = (*StripeAdapter)(nil)

func (a *StripeAdapter) client(ctx context.Context) (*stripe.Client, error) {
	c := a.clients.ForCtx(ctx)
	if c == nil {
		return nil, errors.New("stripe not configured for this mode")
	}
	return c, nil
}

// EnsureStripeCustomer resolves (or lazily creates) the Stripe customer
// for this Velox customer. We store the Stripe customer ID on the 1:1
// customer_payment_setups summary — which we already use for checkout —
// so a portal-created customer and a checkout-created customer share
// the same upstream record.
func (a *StripeAdapter) EnsureStripeCustomer(ctx context.Context, tenantID, customerID string) (string, error) {
	ps, err := a.summary.GetPaymentSetup(ctx, tenantID, customerID)
	if err == nil && ps.StripeCustomerID != "" {
		return ps.StripeCustomerID, nil
	}

	sc, err := a.client(ctx)
	if err != nil {
		return "", err
	}

	cust, err := sc.V1Customers.Create(ctx, &stripe.CustomerCreateParams{
		Params: stripe.Params{
			Metadata: map[string]string{
				"velox_tenant_id":   tenantID,
				"velox_customer_id": customerID,
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("stripe create customer: %w", err)
	}

	// Persist back to the summary so future calls short-circuit.
	_, _ = a.summary.UpsertPaymentSetup(ctx, tenantID, domain.CustomerPaymentSetup{
		CustomerID:       customerID,
		TenantID:         tenantID,
		SetupStatus:      domain.PaymentSetupPending,
		StripeCustomerID: cust.ID,
	})
	return cust.ID, nil
}

func (a *StripeAdapter) CreateSetupIntent(ctx context.Context, stripeCustomerID string, metadata map[string]string) (string, string, error) {
	sc, err := a.client(ctx)
	if err != nil {
		return "", "", err
	}
	si, err := sc.V1SetupIntents.Create(ctx, &stripe.SetupIntentCreateParams{
		Customer:           stripe.String(stripeCustomerID),
		PaymentMethodTypes: stripe.StringSlice([]string{"card"}),
		Usage:              stripe.String("off_session"),
		Params:             stripe.Params{Metadata: metadata},
	})
	if err != nil {
		return "", "", fmt.Errorf("stripe create setup intent: %w", err)
	}
	return si.ClientSecret, si.ID, nil
}

// CreateSetupCheckoutSession creates a Checkout Session in setup mode so
// the customer can be redirected to Stripe's hosted UI to enter card
// details without being charged. Mirrors payment.PortalHandler but for
// the self-serve /me path — the metadata we attach here is what the
// setup_intent.succeeded webhook routes back to the right customer.
func (a *StripeAdapter) CreateSetupCheckoutSession(ctx context.Context, stripeCustomerID, returnURL string, metadata map[string]string) (string, string, error) {
	sc, err := a.client(ctx)
	if err != nil {
		return "", "", err
	}
	sess, err := sc.V1CheckoutSessions.Create(ctx, &stripe.CheckoutSessionCreateParams{
		Customer:           stripe.String(stripeCustomerID),
		Mode:               stripe.String(string(stripe.CheckoutSessionModeSetup)),
		PaymentMethodTypes: stripe.StringSlice([]string{"card"}),
		SuccessURL:         stripe.String(returnURL),
		CancelURL:          stripe.String(returnURL),
		Params:             stripe.Params{Metadata: metadata},
	})
	if err != nil {
		return "", "", fmt.Errorf("stripe create setup checkout session: %w", err)
	}
	return sess.URL, sess.ID, nil
}

func (a *StripeAdapter) DetachPaymentMethod(ctx context.Context, stripePaymentMethodID string) error {
	sc, err := a.client(ctx)
	if err != nil {
		return err
	}
	if _, err := sc.V1PaymentMethods.Detach(ctx, stripePaymentMethodID, nil); err != nil {
		// Stripe returns 404 for "already detached" / "no such payment
		// method". Translate to ErrNotFound so callers can decide
		// whether to treat it as success.
		var se *stripe.Error
		if errors.As(err, &se) && se.HTTPStatusCode == 404 {
			return errs.ErrNotFound
		}
		return fmt.Errorf("stripe detach: %w", err)
	}
	return nil
}

func (a *StripeAdapter) FetchPaymentMethodCard(ctx context.Context, stripePaymentMethodID string) (string, string, int, int, error) {
	sc, err := a.client(ctx)
	if err != nil {
		return "", "", 0, 0, err
	}
	pm, err := sc.V1PaymentMethods.Retrieve(ctx, stripePaymentMethodID, nil)
	if err != nil {
		return "", "", 0, 0, fmt.Errorf("stripe retrieve pm: %w", err)
	}
	if pm.Card == nil {
		return "", "", 0, 0, nil
	}
	return string(pm.Card.Brand), pm.Card.Last4, int(pm.Card.ExpMonth), int(pm.Card.ExpYear), nil
}
