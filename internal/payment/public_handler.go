package payment

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/stripe/stripe-go/v82"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

// PublicPaymentCustomerReader is the narrow hook the public handler
// uses to fetch a customer with PII fields decrypted. The customer
// package owns the encrypt-at-rest wrapper around display_name /
// email; reading through this interface (instead of joining the
// customers table directly) keeps decryption centralised and stops
// the public payment page from rendering raw `enc:…` ciphertext.
type PublicPaymentCustomerReader interface {
	Get(ctx context.Context, tenantID, id string) (domain.Customer, error)
}

// PublicPaymentHandler serves tokenized payment update endpoints.
// These routes require NO auth — the token itself is the credential.
//
// Mode routing: these endpoints are anonymous by design, so there's no
// API-key livemode on the incoming request. The token's referenced
// invoice determines the mode — Token.Validate JOINs invoices to
// resolve livemode in the same query, so the validated token already
// carries everything we need to open a properly-scoped TxTenant for
// follow-on reads. RLS stays as defense-in-depth past the token gate.
type PublicPaymentHandler struct {
	tokens    *TokenService
	db        *postgres.DB
	clients   *StripeClients
	customers PublicPaymentCustomerReader
	returnURL string
}

func NewPublicPaymentHandler(tokens *TokenService, db *postgres.DB, clients *StripeClients, customers PublicPaymentCustomerReader, returnURL string) *PublicPaymentHandler {
	if tokens == nil || !clients.Has() {
		return nil
	}
	return &PublicPaymentHandler{
		tokens:    tokens,
		db:        db,
		clients:   clients,
		customers: customers,
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
	CustomerName   string `json:"customer_name"`
	InvoiceNumber  string `json:"invoice_number"`
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
		// Validate's JOIN to invoices means a deleted-invoice token
		// also lands here as "invalid or expired" — operator-friendly
		// 401 instead of a downstream 500.
		respond.Error(w, r, http.StatusUnauthorized, "authentication_error", "invalid_token",
			"invalid or expired token")
		return
	}

	// All follow-on reads run under TxTenant scoped to the token's
	// resolved (tenant, livemode). RLS stays as defense-in-depth past
	// the token gate — a bug in a WHERE clause cannot return another
	// tenant's row. The token resolves the (tenant, livemode) pair
	// authoritatively at validate time (livemode comes from invoices
	// via the JOIN), so no second bypass-required lookup is needed.
	scopedCtx := postgres.WithLivemode(r.Context(), token.Livemode)
	tx, err := h.db.BeginTx(scopedCtx, postgres.TxTenant, token.TenantID)
	if err != nil {
		slog.ErrorContext(scopedCtx, "public payment: begin tx", "error", err)
		respond.InternalError(w, r)
		return
	}
	defer postgres.Rollback(tx)

	var invoiceNumber, currency string
	var amountDueCents int64
	err = tx.QueryRowContext(scopedCtx, `
		SELECT invoice_number, amount_due_cents, currency FROM invoices WHERE id = $1
	`, token.InvoiceID).Scan(&invoiceNumber, &amountDueCents, &currency)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Validate's JOIN already proved the invoice exists, so
			// ErrNoRows here means it was deleted between Validate and
			// this read — race. Treat as stale link.
			respond.Error(w, r, http.StatusNotFound, "invalid_request_error", "invoice_not_found",
				"this payment link is no longer valid — the invoice it references is unavailable")
			return
		}
		slog.ErrorContext(scopedCtx, "public payment: failed to fetch invoice", "invoice_id", token.InvoiceID, "error", err)
		respond.InternalError(w, r)
		return
	}

	// Customer name comes from the customer service so display_name
	// arrives decrypted. Reading customers.display_name directly here
	// would surface the encrypted ciphertext (`enc:…`) to the public
	// payment page — same architectural issue we fixed for the
	// testclock attached-customers panel.
	cust, err := h.customers.Get(scopedCtx, token.TenantID, token.CustomerID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			respond.Error(w, r, http.StatusNotFound, "invalid_request_error", "customer_not_found",
				"this payment link is no longer valid — the customer it references is unavailable")
			return
		}
		slog.ErrorContext(scopedCtx, "public payment: failed to fetch customer", "customer_id", token.CustomerID, "error", err)
		respond.InternalError(w, r)
		return
	}

	respond.JSON(w, r, http.StatusOK, tokenValidateResponse{
		CustomerName:   cust.DisplayName,
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

	// Look up the customer's Stripe customer ID under TxTenant scoped
	// to the token's resolved (tenant, livemode). RLS stays in play
	// past the token gate; the token's JOIN already resolved livemode
	// authoritatively, so no extra bypass-required lookup is needed.
	scopedCtx := postgres.WithLivemode(r.Context(), token.Livemode)
	tx, err := h.db.BeginTx(scopedCtx, postgres.TxTenant, token.TenantID)
	if err != nil {
		slog.ErrorContext(scopedCtx, "public payment: begin tx", "error", err)
		respond.InternalError(w, r)
		return
	}
	defer postgres.Rollback(tx)

	var stripeCustomerID string
	err = tx.QueryRowContext(scopedCtx, `
		SELECT stripe_customer_id FROM customer_payment_setups
		WHERE customer_id = $1 AND tenant_id = $2
	`, token.CustomerID, token.TenantID).Scan(&stripeCustomerID)
	if err != nil || stripeCustomerID == "" {
		respond.Error(w, r, http.StatusBadRequest, "validation_error", "missing_payment_setup",
			"customer does not have a Stripe payment setup")
		return
	}

	sc := h.clients.For(scopedCtx, token.TenantID, token.Livemode)
	if sc == nil {
		respond.Error(w, r, http.StatusBadGateway, "api_error", "stripe_error",
			"stripe not configured for this mode")
		return
	}

	// Build return URL
	returnURL := h.returnURL
	if returnURL == "" {
		returnURL = fmt.Sprintf("http://localhost:5173/customers/%s?payment=updated", token.CustomerID)
	}

	// Create a Checkout Session in setup mode for new payment method
	sess, err := sc.V1CheckoutSessions.Create(r.Context(), &stripe.CheckoutSessionCreateParams{
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
		slog.ErrorContext(r.Context(), "public payment: failed to create checkout session",
			"customer_id", token.CustomerID, "error", err)
		// Customer-facing endpoint — TIER 2 sanitization (ADR-026):
		// even "Stripe SDK error" framing leaks operator context.
		// End customers should see only neutral copy + reference ID.
		respond.Error(w, r, http.StatusBadGateway, "api_error", "stripe_error",
			"We couldn't start the payment update right now. Please contact your billing administrator if the problem persists.")
		return
	}

	// Mark token as used (single use). Pass scopedCtx (with livemode
	// pinned from the validated token), not raw r.Context() — MarkUsed
	// opens TxTenant internally, and post-ADR-029's fail-closed
	// BeginTx guard returns an error when ctx has no livemode. Using
	// the bare request context here was a copy-paste miss when the
	// scoped variable was introduced; without livemode the token
	// silently failed to mark used, leaving the magic-link reusable.
	if err := h.tokens.MarkUsed(scopedCtx, token.TenantID, rawToken); err != nil {
		slog.ErrorContext(scopedCtx, "public payment: failed to mark token used", "error", err)
		// Non-fatal: session was already created.
	}

	slog.InfoContext(r.Context(), "public payment update session created",
		"customer_id", token.CustomerID,
		"invoice_id", token.InvoiceID,
		"session_id", sess.ID,
	)

	respond.JSON(w, r, http.StatusCreated, checkoutResponse{
		URL: sess.URL,
	})
}
