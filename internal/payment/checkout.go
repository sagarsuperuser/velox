package payment

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/stripe/stripe-go/v82"
	"github.com/stripe/stripe-go/v82/checkout/session"
	stripecustomer "github.com/stripe/stripe-go/v82/customer"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/auth"
)

// CheckoutHandler manages Stripe Checkout Sessions for payment method setup.
type CheckoutHandler struct {
	apiKey     string
	successURL string
	cancelURL  string
}

func NewCheckoutHandler(apiKey, successURL, cancelURL string) *CheckoutHandler {
	if apiKey == "" {
		return nil
	}
	return &CheckoutHandler{apiKey: apiKey, successURL: successURL, cancelURL: cancelURL}
}

func (h *CheckoutHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/setup", h.createSetupSession)
	return r
}

type setupRequest struct {
	CustomerID   string `json:"customer_id"`
	CustomerName string `json:"customer_name"`
	Email        string `json:"email"`
}

type setupResponse struct {
	SessionID  string `json:"session_id"`
	URL        string `json:"url"`
	StripeCustomerID string `json:"stripe_customer_id"`
}

func (h *CheckoutHandler) createSetupSession(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	var req setupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}
	if req.CustomerID == "" {
		respond.Validation(w, r, "customer_id is required")
		return
	}

	stripe.Key = h.apiKey

	// Create or retrieve Stripe customer
	cus, err := stripecustomer.New(&stripe.CustomerParams{
		Name:  stripe.String(req.CustomerName),
		Email: stripe.String(req.Email),
		Params: stripe.Params{
			Metadata: map[string]string{
				"velox_customer_id": req.CustomerID,
				"velox_tenant_id":   tenantID,
			},
		},
	})
	if err != nil {
		respond.Error(w, r, http.StatusBadGateway, "api_error", "stripe_error",
			fmt.Sprintf("failed to create Stripe customer: %v", err))
		return
	}

	// Create checkout session for payment method setup
	successURL := h.successURL
	if successURL == "" {
		successURL = "https://example.com/payment/success?session_id={CHECKOUT_SESSION_ID}"
	}
	cancelURL := h.cancelURL
	if cancelURL == "" {
		cancelURL = "https://example.com/payment/cancel"
	}

	sess, err := session.New(&stripe.CheckoutSessionParams{
		Customer: stripe.String(cus.ID),
		Mode:     stripe.String(string(stripe.CheckoutSessionModeSetup)),
		PaymentMethodTypes: stripe.StringSlice([]string{"card"}),
		SuccessURL: stripe.String(successURL),
		CancelURL:  stripe.String(cancelURL),
		Params: stripe.Params{
			Metadata: map[string]string{
				"velox_customer_id": req.CustomerID,
				"velox_tenant_id":   tenantID,
			},
		},
	})
	if err != nil {
		respond.Error(w, r, http.StatusBadGateway, "api_error", "stripe_error",
			fmt.Sprintf("failed to create checkout session: %v", err))
		return
	}

	respond.JSON(w, r, http.StatusCreated, setupResponse{
		SessionID:        sess.ID,
		URL:              sess.URL,
		StripeCustomerID: cus.ID,
	})
}
