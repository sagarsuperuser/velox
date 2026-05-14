package payment

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stripe/stripe-go/v82"

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
// Mode-aware: picks the live or test client based on the caller's API key.
//
// Return URLs are contextual per request, not global config: the caller
// passes `return_url` in the body and the handler appends
// `?payment=success` or `?payment=cancel`. Matches the rest of the
// Checkout flows in the codebase (PortalHandler, hosted-invoice Pay,
// public update-payment) and follows Stripe's own guidance — cancel =
// "back where you were," not a stateless global page that loses context.
type CheckoutHandler struct {
	clients *StripeClients
	store   PaymentSetupStore
}

func NewCheckoutHandler(clients *StripeClients, store PaymentSetupStore) *CheckoutHandler {
	if !clients.Has() {
		return nil
	}
	return &CheckoutHandler{clients: clients, store: store}
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
	// Address fields for Stripe compliance (required for India)
	AddressLine1   string `json:"address_line1,omitempty"`
	AddressCity    string `json:"address_city,omitempty"`
	AddressState   string `json:"address_state,omitempty"`
	AddressZip     string `json:"address_postal_code,omitempty"`
	AddressCountry string `json:"address_country,omitempty"`
	// ReturnURL is the page the operator came from. The handler appends
	// ?payment=success / ?payment=cancel as a UI hint. If unset, the
	// handler falls back to /customers/{id}?payment=…, which is always
	// a valid page (every setup flow has a customer_id).
	ReturnURL string `json:"return_url,omitempty"`
}

type setupResponse struct {
	SessionID        string `json:"session_id"`
	URL              string `json:"url"`
	StripeCustomerID string `json:"stripe_customer_id"`
}

func (h *CheckoutHandler) createSetupSession(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	sc := h.clients.ForCtx(r.Context())
	if sc == nil {
		respond.Error(w, r, http.StatusBadGateway, "api_error", "stripe_error",
			"stripe not configured for this mode")
		return
	}

	var req setupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}
	if req.CustomerID == "" {
		respond.Validation(w, r, "customer_id is required")
		return
	}

	// Check if customer already has a Stripe customer ID
	var stripeCustomerID string
	if existing, err := h.store.GetPaymentSetup(r.Context(), tenantID, req.CustomerID); err == nil && existing.StripeCustomerID != "" {
		stripeCustomerID = existing.StripeCustomerID
	}

	// Create or update Stripe customer
	if stripeCustomerID == "" {
		cusParams := &stripe.CustomerCreateParams{
			Name:  stripe.String(req.CustomerName),
			Email: stripe.String(req.Email),
			Params: stripe.Params{
				Metadata: map[string]string{
					"velox_customer_id": req.CustomerID,
					"velox_tenant_id":   tenantID,
				},
			},
		}
		// Address required for Indian Stripe accounts (export regulations)
		if req.AddressLine1 != "" || req.AddressCountry != "" {
			cusParams.Address = &stripe.AddressParams{
				Line1:      stripe.String(req.AddressLine1),
				City:       stripe.String(req.AddressCity),
				State:      stripe.String(req.AddressState),
				PostalCode: stripe.String(req.AddressZip),
				Country:    stripe.String(req.AddressCountry),
			}
		}
		cus, err := sc.V1Customers.Create(r.Context(), cusParams)
		if err != nil {
			respond.Error(w, r, http.StatusBadGateway, "api_error", "stripe_error",
				fmt.Sprintf("failed to create Stripe customer: %v", err))
			return
		}
		stripeCustomerID = cus.ID
	} else {
		// Always sync latest customer data to Stripe on re-setup
		updateParams := &stripe.CustomerUpdateParams{
			Name:  stripe.String(req.CustomerName),
			Email: stripe.String(req.Email),
		}
		if req.AddressLine1 != "" || req.AddressCountry != "" {
			updateParams.Address = &stripe.AddressParams{
				Line1:      stripe.String(req.AddressLine1),
				City:       stripe.String(req.AddressCity),
				State:      stripe.String(req.AddressState),
				PostalCode: stripe.String(req.AddressZip),
				Country:    stripe.String(req.AddressCountry),
			}
		}
		_, _ = sc.V1Customers.Update(r.Context(), stripeCustomerID, updateParams)
	}

	// Save Stripe customer ID immediately (status: pending until checkout completes)
	now := time.Now().UTC()
	_, _ = h.store.UpsertPaymentSetup(r.Context(), tenantID, domain.CustomerPaymentSetup{
		CustomerID:       req.CustomerID,
		TenantID:         tenantID,
		SetupStatus:      domain.PaymentSetupPending,
		StripeCustomerID: stripeCustomerID,
		UpdatedAt:        now,
	})

	// Build contextual return URLs. If the caller passed return_url
	// (the page they came from), use it; otherwise default to the
	// customer's detail page. Either way the customer_id is in the
	// URL so the SPA can refetch payment_setup and show the result.
	base := req.ReturnURL
	if base == "" {
		base = fmt.Sprintf("http://localhost:5173/customers/%s", req.CustomerID)
	}
	successURL := appendQuery(base, "payment", "success")
	cancelURL := appendQuery(base, "payment", "cancel")

	sess, err := sc.V1CheckoutSessions.Create(r.Context(), &stripe.CheckoutSessionCreateParams{
		Customer:           stripe.String(stripeCustomerID),
		Mode:               stripe.String(string(stripe.CheckoutSessionModeSetup)),
		PaymentMethodTypes: stripe.StringSlice([]string{"card"}),
		SuccessURL:         stripe.String(successURL),
		CancelURL:          stripe.String(cancelURL),
		Params: stripe.Params{
			Metadata: map[string]string{
				"velox_customer_id": req.CustomerID,
				"velox_tenant_id":   tenantID,
			},
		},
	})
	if err != nil {
		// Sanitize (ADR-026): the Stripe SDK error includes raw
		// API request/response bodies. Log full server-side; surface
		// generic operator-safe message.
		slog.ErrorContext(r.Context(), "stripe checkout session create failed",
			"customer_id", req.CustomerID, "error", err)
		respond.Error(w, r, http.StatusBadGateway, "api_error", "stripe_error",
			"Could not start the payment setup flow. Please retry; if the problem persists, contact support.")
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

// appendQuery adds key=value to base, preserving any existing query
// string. Used to stamp `?payment=success` / `?payment=cancel` onto
// the operator's source page so the SPA knows how to render the result.
func appendQuery(base, key, value string) string {
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return base + sep + key + "=" + value
}
