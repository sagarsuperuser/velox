package payment

import (
	"context"
	"errors"
	"fmt"

	"github.com/stripe/stripe-go/v82"

	"github.com/sagarsuperuser/velox/internal/errs"
)

// stripeErrorMessage extracts a clean human-readable message from Stripe SDK
// errors. All output is run through errs.Scrub so card last4, raw PANs, and
// emails can't leak into the invoice.last_payment_error column or slog
// fields downstream. Scrubbing at this single ingress point means every
// caller (persistence, logging, HTTP responses) is automatically safe.
func stripeErrorMessage(err error) string {
	var stripeErr *stripe.Error
	if errors.As(err, &stripeErr) {
		if stripeErr.Msg != "" {
			return errs.Scrub(stripeErr.Msg)
		}
		return string(stripeErr.Code)
	}
	return errs.Scrub(err.Error())
}

// PaymentError categorises a failed PaymentIntent attempt. Unknown=true means
// the request's outcome could not be determined from the response (5xx, network
// drop, timeout) — callers must NOT treat this as a decline, because Stripe
// may have processed the charge server-side. A reconciler resolves these by
// querying Stripe after a cool-off window.
type PaymentError struct {
	Message         string
	DeclineCode     string // non-empty for card declines (card_declined, insufficient_funds, etc.)
	PaymentIntentID string // set when Stripe returned a PI object alongside the error
	Unknown         bool
}

func (e *PaymentError) Error() string { return e.Message }

// ErrPaymentTransient signals that the payment attempt was short-circuited
// upstream of Stripe (circuit breaker open, timeout fired before the call)
// — the Stripe API was not invoked, so the invoice state is unchanged. The
// scheduler/dunning path should treat this as "skip this tick" and not
// tick the dunning attempt count, unlike a real payment failure.
var ErrPaymentTransient = errors.New("payment attempt skipped; upstream breaker open or timeout before Stripe")

// classifyStripeError maps a stripe-go SDK error to a PaymentError. Non-Stripe
// errors (context cancel, DNS failure wrapped by our code, etc.) are treated
// as unknown, because we cannot prove the request never reached Stripe.
func classifyStripeError(err error) *PaymentError {
	if err == nil {
		return nil
	}
	var stripeErr *stripe.Error
	if !errors.As(err, &stripeErr) {
		return &PaymentError{Message: errs.Scrub(err.Error()), Unknown: true}
	}

	pe := &PaymentError{
		Message:     stripeErrorMessage(err),
		DeclineCode: string(stripeErr.DeclineCode),
	}
	if stripeErr.PaymentIntent != nil {
		pe.PaymentIntentID = stripeErr.PaymentIntent.ID
	}

	switch stripeErr.Type {
	case stripe.ErrorTypeCard,
		stripe.ErrorTypeInvalidRequest,
		stripe.ErrorTypeIdempotency:
		// Stripe explicitly rejected the request; no charge occurred.
		pe.Unknown = false
	case stripe.ErrorTypeAPI:
		// 5xx from Stripe — request may or may not have been processed.
		pe.Unknown = true
	default:
		// Unknown error type — fail safe, treat as ambiguous.
		pe.Unknown = true
	}
	return pe
}

// ErrStripeNotConfigured is returned when a request routes to a Stripe mode
// (live or test) for which no secret key is configured. Surfaces as an
// explicit PaymentError rather than a nil deref so operators get an
// actionable signal ("your test-mode key tried to charge, set STRIPE_SECRET_KEY_TEST").
var ErrStripeNotConfigured = &PaymentError{Message: "stripe not configured for this mode"}

// LiveStripeClient wraps the Stripe SDK for PaymentIntent operations. Despite
// the "Live" in the name, it handles both live and test modes — it selects
// the underlying per-mode client from clients.ForCtx(ctx) on each call.
// Implements the StripeClient interface used by the payment adapter.
type LiveStripeClient struct {
	clients *StripeClients
}

// NewLiveStripeClient creates a client from the mode-aware StripeClients
// bundle. Returns nil if clients is nil or has no configured modes.
func NewLiveStripeClient(clients *StripeClients) *LiveStripeClient {
	if !clients.Has() {
		return nil
	}
	return &LiveStripeClient{clients: clients}
}

func (c *LiveStripeClient) CreatePaymentIntent(ctx context.Context, params PaymentIntentParams) (PaymentIntentResult, error) {
	sc := c.clients.ForCtx(ctx)
	if sc == nil {
		return PaymentIntentResult{}, ErrStripeNotConfigured
	}

	metadata := make(map[string]string)
	for k, v := range params.Metadata {
		metadata[k] = v
	}

	// Use the customer's default payment method, fall back to most recent card
	cus, err := sc.V1Customers.Retrieve(ctx, params.CustomerID, nil)
	var defaultPM string
	if err == nil && cus.InvoiceSettings != nil && cus.InvoiceSettings.DefaultPaymentMethod != nil {
		defaultPM = cus.InvoiceSettings.DefaultPaymentMethod.ID
	}
	if defaultPM == "" {
		// Fall back to most recently created card
		var latest *stripe.PaymentMethod
		for pm, err := range sc.V1PaymentMethods.List(ctx, &stripe.PaymentMethodListParams{
			Customer: stripe.String(params.CustomerID),
			Type:     stripe.String("card"),
		}) {
			if err != nil {
				break
			}
			if latest == nil || pm.Created > latest.Created {
				latest = pm
			}
		}
		if latest != nil {
			defaultPM = latest.ID
		}
	}
	if defaultPM == "" {
		// Definitive failure — no card on file, no charge could have occurred.
		return PaymentIntentResult{}, &PaymentError{Message: "customer has no payment method on file"}
	}

	pi, err := sc.V1PaymentIntents.Create(ctx, &stripe.PaymentIntentCreateParams{
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
		return PaymentIntentResult{}, classifyStripeError(err)
	}

	return PaymentIntentResult{
		ID:           pi.ID,
		Status:       string(pi.Status),
		ClientSecret: pi.ClientSecret,
	}, nil
}

func (c *LiveStripeClient) FetchCardDetails(ctx context.Context, stripeCustomerID string) (CardDetails, error) {
	sc := c.clients.ForCtx(ctx)
	if sc == nil {
		return CardDetails{}, ErrStripeNotConfigured
	}

	// Get the most recently created card
	var latest *stripe.PaymentMethod
	for pm, err := range sc.V1PaymentMethods.List(ctx, &stripe.PaymentMethodListParams{
		Customer: stripe.String(stripeCustomerID),
		Type:     stripe.String("card"),
	}) {
		if err != nil {
			break
		}
		if latest == nil || pm.Created > latest.Created {
			latest = pm
		}
	}
	if latest == nil || latest.Card == nil {
		return CardDetails{}, fmt.Errorf("no card found for customer %s", stripeCustomerID)
	}

	// Set this card as the customer's default payment method
	_, _ = sc.V1Customers.Update(ctx, stripeCustomerID, &stripe.CustomerUpdateParams{
		InvoiceSettings: &stripe.CustomerUpdateInvoiceSettingsParams{
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

func (c *LiveStripeClient) CancelPaymentIntent(ctx context.Context, paymentIntentID string) error {
	sc := c.clients.ForCtx(ctx)
	if sc == nil {
		return ErrStripeNotConfigured
	}
	_, err := sc.V1PaymentIntents.Cancel(ctx, paymentIntentID, nil)
	if err != nil {
		return fmt.Errorf("stripe cancel: %s", stripeErrorMessage(err))
	}
	return nil
}

// GetPaymentIntent fetches the current state of a PaymentIntent. Used by the
// reconciler to resolve PaymentUnknown invoices — Stripe is the source of
// truth for whether a charge actually succeeded.
func (c *LiveStripeClient) GetPaymentIntent(ctx context.Context, paymentIntentID string) (PaymentIntentResult, error) {
	sc := c.clients.ForCtx(ctx)
	if sc == nil {
		return PaymentIntentResult{}, ErrStripeNotConfigured
	}
	pi, err := sc.V1PaymentIntents.Retrieve(ctx, paymentIntentID, nil)
	if err != nil {
		return PaymentIntentResult{}, classifyStripeError(err)
	}
	return PaymentIntentResult{
		ID:           pi.ID,
		Status:       string(pi.Status),
		ClientSecret: pi.ClientSecret,
	}, nil
}
