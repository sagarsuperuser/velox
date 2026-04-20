package paymentmethods

import (
	"context"
	"errors"
	"fmt"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

// StripeAPI is the narrow subset of Stripe we need. Declared here instead
// of depending on internal/payment so the paymentmethods package can be
// tested with a fake and so internal/payment doesn't gain a reverse
// dependency.
type StripeAPI interface {
	// CreateSetupIntent makes a SetupIntent for the Stripe customer and
	// returns the client_secret the frontend needs for confirmCardSetup().
	// Used by integrations that build an inline Stripe Elements UI.
	CreateSetupIntent(ctx context.Context, stripeCustomerID string, metadata map[string]string) (clientSecret, setupIntentID string, err error)

	// CreateSetupCheckoutSession creates a Stripe Checkout Session in
	// setup mode and returns a hosted URL the customer can redirect to.
	// Used by the default web-v2 portal UI, which redirects rather than
	// embedding Stripe Elements.
	CreateSetupCheckoutSession(ctx context.Context, stripeCustomerID, returnURL string, metadata map[string]string) (checkoutURL, sessionID string, err error)

	// EnsureStripeCustomer returns the existing Stripe customer ID from
	// customer_payment_setups, or creates one if absent and writes it back
	// to the setup row. Needed because a customer might not have a Stripe
	// customer yet when they first land on the portal.
	EnsureStripeCustomer(ctx context.Context, tenantID, customerID string) (string, error)

	// DetachPaymentMethod calls Stripe's detach endpoint. Best-effort — if
	// Stripe has already detached (e.g. card expired and Stripe removed
	// it), we still want to mark the local row detached.
	DetachPaymentMethod(ctx context.Context, stripePaymentMethodID string) error

	// FetchPaymentMethodCard looks up card metadata (brand/last4/exp) for
	// a Stripe PM. Used by the webhook handler in P5 when persisting a
	// newly attached PM.
	FetchPaymentMethodCard(ctx context.Context, stripePaymentMethodID string) (brand, last4 string, expMonth, expYear int, err error)
}

// PaymentSetupSummaryWriter updates the 1:1 customer_payment_setups row.
// We keep it as a denorm of the current default so billing's existing
// read path (which knows nothing about payment_methods) keeps working.
type PaymentSetupSummaryWriter interface {
	UpsertPaymentSetup(ctx context.Context, tenantID string, ps domain.CustomerPaymentSetup) (domain.CustomerPaymentSetup, error)
	GetPaymentSetup(ctx context.Context, tenantID, customerID string) (domain.CustomerPaymentSetup, error)
}

type Service struct {
	store         Store
	stripe        StripeAPI
	summary       PaymentSetupSummaryWriter
	portalBaseURL string // optional; used as return_url fallback when the handler doesn't pass one
}

func NewService(store Store, stripe StripeAPI, summary PaymentSetupSummaryWriter) *Service {
	return &Service{store: store, stripe: stripe, summary: summary}
}

// List returns active PMs for (tenantID, customerID). Ordered default
// first for UI convenience.
func (s *Service) List(ctx context.Context, tenantID, customerID string) ([]PaymentMethod, error) {
	return s.store.List(ctx, tenantID, customerID)
}

// CreateSetupIntent returns the client_secret a browser needs to run
// stripe.confirmCardSetup(). The actual payment_methods row is written by
// the webhook handler once Stripe confirms the setup — we don't trust
// the browser's "success" callback.
func (s *Service) CreateSetupIntent(ctx context.Context, tenantID, customerID string) (clientSecret, setupIntentID string, err error) {
	if tenantID == "" || customerID == "" {
		return "", "", errs.Required("tenant_id, customer_id")
	}
	stripeCustomerID, err := s.stripe.EnsureStripeCustomer(ctx, tenantID, customerID)
	if err != nil {
		return "", "", fmt.Errorf("ensure stripe customer: %w", err)
	}
	metadata := map[string]string{
		"velox_tenant_id":   tenantID,
		"velox_customer_id": customerID,
		"velox_livemode":    livemodeLabel(ctx),
	}
	return s.stripe.CreateSetupIntent(ctx, stripeCustomerID, metadata)
}

// CreateSetupSession returns a hosted Stripe Checkout URL the customer can
// be redirected to for adding a new card without being charged. On
// success Stripe fires setup_intent.succeeded, which the webhook handler
// turns into a payment_methods row via AttachForWebhook.
func (s *Service) CreateSetupSession(ctx context.Context, tenantID, customerID, returnURL string) (checkoutURL, sessionID string, err error) {
	if tenantID == "" || customerID == "" {
		return "", "", errs.Required("tenant_id, customer_id")
	}
	stripeCustomerID, err := s.stripe.EnsureStripeCustomer(ctx, tenantID, customerID)
	if err != nil {
		return "", "", fmt.Errorf("ensure stripe customer: %w", err)
	}
	if returnURL == "" {
		returnURL = s.portalBaseURL
	}
	if returnURL == "" {
		returnURL = "http://localhost:5173/customer-portal"
	}
	metadata := map[string]string{
		"velox_tenant_id":   tenantID,
		"velox_customer_id": customerID,
		"velox_livemode":    livemodeLabel(ctx),
		"velox_purpose":     "portal_add_payment_method",
	}
	return s.stripe.CreateSetupCheckoutSession(ctx, stripeCustomerID, returnURL, metadata)
}

// SetDefault flips is_default atomically in payment_methods AND refreshes
// the customer_payment_setups summary row so billing sees the new card.
func (s *Service) SetDefault(ctx context.Context, tenantID, customerID, pmID string) (PaymentMethod, error) {
	pm, err := s.store.SetDefault(ctx, tenantID, customerID, pmID)
	if err != nil {
		return PaymentMethod{}, err
	}
	if err := s.syncSummary(ctx, tenantID, pm); err != nil {
		return PaymentMethod{}, fmt.Errorf("sync payment setup summary: %w", err)
	}
	return pm, nil
}

// Detach marks the PM detached both in Stripe and locally, then — if the
// detached PM was the default — picks a replacement default (most recent
// attached PM) or clears the summary if no PMs remain.
func (s *Service) Detach(ctx context.Context, tenantID, customerID, pmID string) (PaymentMethod, error) {
	pm, err := s.store.Get(ctx, tenantID, pmID)
	if err != nil {
		return PaymentMethod{}, err
	}
	if pm.CustomerID != customerID {
		return PaymentMethod{}, errs.ErrNotFound
	}

	if err := s.stripe.DetachPaymentMethod(ctx, pm.StripePaymentMethodID); err != nil {
		// Stripe returns 404 if already detached — treat that as success.
		// Any other error bubbles up. We use errors.Is on a best-effort
		// basis; most implementations wrap with enough context.
		if !errors.Is(err, errs.ErrNotFound) {
			return PaymentMethod{}, fmt.Errorf("stripe detach: %w", err)
		}
	}

	detached, err := s.store.Detach(ctx, tenantID, customerID, pmID)
	if err != nil {
		return PaymentMethod{}, err
	}

	// If the detached card was the default, promote another PM if one
	// exists; otherwise clear the summary.
	if pm.IsDefault {
		if err := s.rebalanceDefault(ctx, tenantID, customerID); err != nil {
			return PaymentMethod{}, fmt.Errorf("rebalance default: %w", err)
		}
	}

	return detached, nil
}

// rebalanceDefault is called after detaching the current default. It
// promotes the newest active PM, or — if none remain — clears the
// summary row back to "missing".
func (s *Service) rebalanceDefault(ctx context.Context, tenantID, customerID string) error {
	active, err := s.store.List(ctx, tenantID, customerID)
	if err != nil {
		return err
	}
	if len(active) == 0 {
		return s.clearSummary(ctx, tenantID, customerID)
	}
	// List orders is_default DESC, created_at DESC — but all are now
	// is_default=false (we just cleared the prior default). Pick [0] (most
	// recent created_at) as the new default.
	promoted, err := s.store.SetDefault(ctx, tenantID, customerID, active[0].ID)
	if err != nil {
		return err
	}
	return s.syncSummary(ctx, tenantID, promoted)
}

func (s *Service) syncSummary(ctx context.Context, tenantID string, pm PaymentMethod) error {
	existing, _ := s.summary.GetPaymentSetup(ctx, tenantID, pm.CustomerID)
	existing.CustomerID = pm.CustomerID
	existing.TenantID = tenantID
	existing.SetupStatus = domain.PaymentSetupReady
	existing.DefaultPaymentMethodPresent = true
	existing.PaymentMethodType = pm.Type
	existing.StripePaymentMethodID = pm.StripePaymentMethodID
	existing.CardBrand = pm.CardBrand
	existing.CardLast4 = pm.CardLast4
	existing.CardExpMonth = pm.CardExpMonth
	existing.CardExpYear = pm.CardExpYear
	_, err := s.summary.UpsertPaymentSetup(ctx, tenantID, existing)
	return err
}

func (s *Service) clearSummary(ctx context.Context, tenantID, customerID string) error {
	existing, _ := s.summary.GetPaymentSetup(ctx, tenantID, customerID)
	existing.CustomerID = customerID
	existing.TenantID = tenantID
	existing.SetupStatus = domain.PaymentSetupMissing
	existing.DefaultPaymentMethodPresent = false
	existing.StripePaymentMethodID = ""
	existing.CardBrand = ""
	existing.CardLast4 = ""
	existing.CardExpMonth = 0
	existing.CardExpYear = 0
	_, err := s.summary.UpsertPaymentSetup(ctx, tenantID, existing)
	return err
}

// AttachForWebhook is the error-only variant of AttachFromSetupIntent,
// used by payment.Stripe.HandleWebhook which doesn't need the PM row. Keeps
// the webhook-facing signature narrow so payment/ doesn't have to know
// about PaymentMethod.
func (s *Service) AttachForWebhook(ctx context.Context, tenantID, customerID, stripePaymentMethodID string) error {
	_, err := s.AttachFromSetupIntent(ctx, tenantID, customerID, stripePaymentMethodID)
	return err
}

// AttachFromSetupIntent is the entry point the P5 webhook handler uses:
// after setup_intent.succeeded, we know the PM and customer, and we
// persist the row here. Called with an RLS ctx already staged to the
// right tenant+livemode by the webhook handler.
func (s *Service) AttachFromSetupIntent(ctx context.Context, tenantID, customerID, stripePaymentMethodID string) (PaymentMethod, error) {
	brand, last4, expMonth, expYear, err := s.stripe.FetchPaymentMethodCard(ctx, stripePaymentMethodID)
	if err != nil {
		return PaymentMethod{}, fmt.Errorf("fetch card metadata: %w", err)
	}
	pm, err := s.store.Upsert(ctx, tenantID, PaymentMethod{
		CustomerID:            customerID,
		StripePaymentMethodID: stripePaymentMethodID,
		Type:                  "card",
		CardBrand:             brand,
		CardLast4:             last4,
		CardExpMonth:          expMonth,
		CardExpYear:           expYear,
	})
	if err != nil {
		return PaymentMethod{}, err
	}
	if pm.IsDefault {
		if err := s.syncSummary(ctx, tenantID, pm); err != nil {
			return PaymentMethod{}, fmt.Errorf("sync summary: %w", err)
		}
	}
	return pm, nil
}

func livemodeLabel(ctx context.Context) string {
	if postgres.Livemode(ctx) {
		return "true"
	}
	return "false"
}
