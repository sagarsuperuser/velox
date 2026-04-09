package payment

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stripe/stripe-go/v82"
	"github.com/stripe/stripe-go/v82/checkout/session"
	stripecustomer "github.com/stripe/stripe-go/v82/customer"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/domain"
)

// PaymentSetupStore persists Stripe customer/payment method data.
type PaymentSetupStore interface {
	UpsertPaymentSetup(ctx context.Context, tenantID string, ps domain.CustomerPaymentSetup) (domain.CustomerPaymentSetup, error)
	GetPaymentSetup(ctx context.Context, tenantID, customerID string) (domain.CustomerPaymentSetup, error)
}

// CheckoutHandler manages Stripe Checkout Sessions for payment method setup.
type CheckoutHandler struct {
	apiKey     string
	successURL string
	cancelURL  string
	store      PaymentSetupStore
}

func NewCheckoutHandler(apiKey, successURL, cancelURL string, store PaymentSetupStore) *CheckoutHandler {
	if apiKey == "" {
		return nil
	}
	return &CheckoutHandler{apiKey: apiKey, successURL: successURL, cancelURL: cancelURL, store: store}
}

func (h *CheckoutHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/setup", h.createSetupSession)
	r.Get("/status/{customerID}", h.getPaymentStatus)
	return r
}

type setupRequest struct {
	CustomerID   string `json:"customer_id"`
	CustomerName string `json:"customer_name"`
	Email        string `json:"email"`
}

type setupResponse struct {
	SessionID        string `json:"session_id"`
	URL              string `json:"url"`
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

	// Check if customer already has a Stripe customer ID
	var stripeCustomerID string
	if existing, err := h.store.GetPaymentSetup(r.Context(), tenantID, req.CustomerID); err == nil && existing.StripeCustomerID != "" {
		stripeCustomerID = existing.StripeCustomerID
	}

	// Create Stripe customer if needed
	if stripeCustomerID == "" {
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
		stripeCustomerID = cus.ID
	}

	// Save Stripe customer ID immediately (status: pending until checkout completes)
	now := time.Now().UTC()
	h.store.UpsertPaymentSetup(r.Context(), tenantID, domain.CustomerPaymentSetup{
		CustomerID:       req.CustomerID,
		TenantID:         tenantID,
		SetupStatus:      domain.PaymentSetupPending,
		StripeCustomerID: stripeCustomerID,
		UpdatedAt:        now,
	})

	// Create checkout session for payment method setup
	successURL := h.successURL
	if successURL == "" {
		successURL = fmt.Sprintf("http://localhost:3000/customers/%s?payment=success", req.CustomerID)
	}
	cancelURL := h.cancelURL
	if cancelURL == "" {
		cancelURL = fmt.Sprintf("http://localhost:3000/customers/%s?payment=cancel", req.CustomerID)
	}

	sess, err := session.New(&stripe.CheckoutSessionParams{
		Customer: stripe.String(stripeCustomerID),
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
		StripeCustomerID: stripeCustomerID,
	})
}

func (h *CheckoutHandler) getPaymentStatus(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	customerID := chi.URLParam(r, "customerID")

	ps, err := h.store.GetPaymentSetup(r.Context(), tenantID, customerID)
	if err != nil {
		respond.JSON(w, r, http.StatusOK, domain.CustomerPaymentSetup{
			CustomerID:  customerID,
			SetupStatus: domain.PaymentSetupMissing,
		})
		return
	}

	respond.JSON(w, r, http.StatusOK, ps)
}
