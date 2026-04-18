package payment

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stripe/stripe-go/v82"
	"github.com/stripe/stripe-go/v82/checkout/session"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/domain"
)

// PortalHandler manages customer self-service payment method updates.
// It creates Stripe Checkout Sessions in "setup" mode so customers can
// replace their card without being charged.
type PortalHandler struct {
	apiKey string
	store  PaymentSetupStore
}

func NewPortalHandler(apiKey string, store PaymentSetupStore) *PortalHandler {
	if apiKey == "" {
		return nil
	}
	return &PortalHandler{apiKey: apiKey, store: store}
}

func (h *PortalHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/{customerID}/update-payment-method", h.createUpdateSession)
	return r
}

type updatePaymentMethodRequest struct {
	ReturnURL string `json:"return_url,omitempty"`
}

type updatePaymentMethodResponse struct {
	URL string `json:"url"`
}

func (h *PortalHandler) createUpdateSession(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	customerID := chi.URLParam(r, "customerID")

	// Parse optional return URL from request body (body may be empty)
	var req updatePaymentMethodRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req) // ignore decode errors — fields are optional
	}

	// Look up the customer's existing Stripe customer ID
	ps, err := h.store.GetPaymentSetup(r.Context(), tenantID, customerID)
	if err != nil || ps.StripeCustomerID == "" {
		respond.Error(w, r, http.StatusBadRequest, "validation_error", "missing_payment_setup",
			"Customer does not have a Stripe payment setup. Use /checkout/setup first.")
		return
	}

	stripe.Key = h.apiKey

	// Build return URL
	returnURL := req.ReturnURL
	if returnURL == "" {
		returnURL = fmt.Sprintf("http://localhost:5173/customers/%s?payment=updated", customerID)
	}

	// Create a Checkout Session in setup mode to collect a new payment method
	sess, err := session.New(&stripe.CheckoutSessionParams{
		Customer:           stripe.String(ps.StripeCustomerID),
		Mode:               stripe.String(string(stripe.CheckoutSessionModeSetup)),
		PaymentMethodTypes: stripe.StringSlice([]string{"card"}),
		SuccessURL:         stripe.String(returnURL),
		CancelURL:          stripe.String(returnURL),
		Params: stripe.Params{
			Metadata: map[string]string{
				"velox_customer_id": customerID,
				"velox_tenant_id":   tenantID,
				"velox_purpose":     "update_payment_method",
			},
		},
	})
	if err != nil {
		respond.Error(w, r, http.StatusBadGateway, "api_error", "stripe_error",
			fmt.Sprintf("failed to create checkout session: %v", err))
		return
	}

	// Mark payment setup as pending update
	now := time.Now().UTC()
	_, _ = h.store.UpsertPaymentSetup(r.Context(), tenantID, domain.CustomerPaymentSetup{
		CustomerID:       customerID,
		TenantID:         tenantID,
		SetupStatus:      domain.PaymentSetupPending,
		StripeCustomerID: ps.StripeCustomerID,
		// Preserve existing card details until new ones arrive
		CardBrand:                   ps.CardBrand,
		CardLast4:                   ps.CardLast4,
		CardExpMonth:                ps.CardExpMonth,
		CardExpYear:                 ps.CardExpYear,
		StripePaymentMethodID:       ps.StripePaymentMethodID,
		DefaultPaymentMethodPresent: ps.DefaultPaymentMethodPresent,
		PaymentMethodType:           ps.PaymentMethodType,
		UpdatedAt:                   now,
	})

	slog.Info("payment method update session created",
		"customer_id", customerID,
		"stripe_customer_id", ps.StripeCustomerID,
		"session_id", sess.ID,
	)

	respond.JSON(w, r, http.StatusCreated, updatePaymentMethodResponse{
		URL: sess.URL,
	})
}
