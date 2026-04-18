package payment

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/stripe/stripe-go/v82"
	"github.com/stripe/stripe-go/v82/checkout/session"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

// PublicPaymentHandler serves tokenized payment update endpoints.
// These routes require NO auth — the token itself is the credential.
type PublicPaymentHandler struct {
	tokens    *TokenService
	db        *postgres.DB
	apiKey    string
	returnURL string
}

func NewPublicPaymentHandler(tokens *TokenService, db *postgres.DB, apiKey, returnURL string) *PublicPaymentHandler {
	if tokens == nil || apiKey == "" {
		return nil
	}
	return &PublicPaymentHandler{
		tokens:    tokens,
		db:        db,
		apiKey:    apiKey,
		returnURL: returnURL,
	}
}

func (h *PublicPaymentHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/{token}", h.validateToken)
	r.Post("/{token}/checkout", h.createCheckoutSession)
	return r
}

type tokenValidateResponse struct {
	CustomerName string `json:"customer_name"`
	InvoiceNumber string `json:"invoice_number"`
	AmountDueCents int64  `json:"amount_due_cents"`
	Currency       string `json:"currency"`
}

func (h *PublicPaymentHandler) validateToken(w http.ResponseWriter, r *http.Request) {
	rawToken := chi.URLParam(r, "token")
	if rawToken == "" {
		respond.BadRequest(w, r, "token is required")
		return
	}

	token, err := h.tokens.Validate(r.Context(), rawToken)
	if err != nil {
		respond.Error(w, r, http.StatusUnauthorized, "authentication_error", "invalid_token",
			"invalid or expired token")
		return
	}

	// Query invoice details
	var invoiceNumber, currency string
	var amountDueCents int64
	err = h.db.Pool.QueryRowContext(r.Context(), `
		SELECT invoice_number, amount_due_cents, currency FROM invoices WHERE id = $1
	`, token.InvoiceID).Scan(&invoiceNumber, &amountDueCents, &currency)
	if err != nil {
		slog.Error("public payment: failed to fetch invoice", "invoice_id", token.InvoiceID, "error", err)
		respond.InternalError(w, r)
		return
	}

	// Query customer display name
	var customerName string
	err = h.db.Pool.QueryRowContext(r.Context(), `
		SELECT display_name FROM customers WHERE id = $1
	`, token.CustomerID).Scan(&customerName)
	if err != nil {
		slog.Error("public payment: failed to fetch customer", "customer_id", token.CustomerID, "error", err)
		respond.InternalError(w, r)
		return
	}

	respond.JSON(w, r, http.StatusOK, tokenValidateResponse{
		CustomerName:   customerName,
		InvoiceNumber:  invoiceNumber,
		AmountDueCents: amountDueCents,
		Currency:       currency,
	})
}

type checkoutResponse struct {
	URL string `json:"url"`
}

func (h *PublicPaymentHandler) createCheckoutSession(w http.ResponseWriter, r *http.Request) {
	rawToken := chi.URLParam(r, "token")
	if rawToken == "" {
		respond.BadRequest(w, r, "token is required")
		return
	}

	token, err := h.tokens.Validate(r.Context(), rawToken)
	if err != nil {
		respond.Error(w, r, http.StatusUnauthorized, "authentication_error", "invalid_token",
			"invalid or expired token")
		return
	}

	// Look up the customer's Stripe customer ID
	var stripeCustomerID string
	err = h.db.Pool.QueryRowContext(r.Context(), `
		SELECT stripe_customer_id FROM customer_payment_setups
		WHERE customer_id = $1 AND tenant_id = $2
	`, token.CustomerID, token.TenantID).Scan(&stripeCustomerID)
	if err != nil || stripeCustomerID == "" {
		respond.Error(w, r, http.StatusBadRequest, "validation_error", "missing_payment_setup",
			"customer does not have a Stripe payment setup")
		return
	}

	// Build return URL
	returnURL := h.returnURL
	if returnURL == "" {
		returnURL = fmt.Sprintf("http://localhost:5173/customers/%s?payment=updated", token.CustomerID)
	}

	stripe.Key = h.apiKey

	// Create a Checkout Session in setup mode for new payment method
	sess, err := session.New(&stripe.CheckoutSessionParams{
		Customer:           stripe.String(stripeCustomerID),
		Mode:               stripe.String(string(stripe.CheckoutSessionModeSetup)),
		PaymentMethodTypes: stripe.StringSlice([]string{"card"}),
		SuccessURL:         stripe.String(returnURL),
		CancelURL:          stripe.String(returnURL),
		Params: stripe.Params{
			Metadata: map[string]string{
				"velox_customer_id": token.CustomerID,
				"velox_tenant_id":   token.TenantID,
				"velox_invoice_id":  token.InvoiceID,
				"velox_purpose":     "payment_update_token",
			},
		},
	})
	if err != nil {
		slog.Error("public payment: failed to create checkout session",
			"customer_id", token.CustomerID, "error", err)
		respond.Error(w, r, http.StatusBadGateway, "api_error", "stripe_error",
			fmt.Sprintf("failed to create checkout session: %v", err))
		return
	}

	// Mark token as used (single use)
	if err := h.tokens.MarkUsed(r.Context(), rawToken); err != nil {
		slog.Error("public payment: failed to mark token used", "error", err)
		// Non-fatal: session was already created
	}

	slog.Info("public payment update session created",
		"customer_id", token.CustomerID,
		"invoice_id", token.InvoiceID,
		"session_id", sess.ID,
	)

	respond.JSON(w, r, http.StatusCreated, checkoutResponse{
		URL: sess.URL,
	})
}
