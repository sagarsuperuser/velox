package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/stripe/stripe-go/v82"

	"github.com/sagarsuperuser/velox/internal/billing"
	"github.com/sagarsuperuser/velox/internal/credit"
	"github.com/sagarsuperuser/velox/internal/creditnote"
	"github.com/sagarsuperuser/velox/internal/customer"
	"github.com/sagarsuperuser/velox/internal/customerportal"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/dunning"
	"github.com/sagarsuperuser/velox/internal/invoice"
	"github.com/sagarsuperuser/velox/internal/payment"
	"github.com/sagarsuperuser/velox/internal/platform/crypto"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
	"github.com/sagarsuperuser/velox/internal/subscription"
)

// invoiceWriterAdapter bridges invoice.PostgresStore → billing.InvoiceWriter.
type invoiceWriterAdapter struct {
	store *invoice.PostgresStore
}

func (a *invoiceWriterAdapter) CreateInvoice(ctx context.Context, tenantID string, inv domain.Invoice) (domain.Invoice, error) {
	return a.store.Create(ctx, tenantID, inv)
}

func (a *invoiceWriterAdapter) CreateLineItem(ctx context.Context, tenantID string, item domain.InvoiceLineItem) (domain.InvoiceLineItem, error) {
	return a.store.CreateLineItem(ctx, tenantID, item)
}

func (a *invoiceWriterAdapter) ApplyCreditAmount(ctx context.Context, tenantID, id string, amountCents int64) (domain.Invoice, error) {
	return a.store.ApplyCredits(ctx, tenantID, id, amountCents)
}

func (a *invoiceWriterAdapter) GetInvoice(ctx context.Context, tenantID, id string) (domain.Invoice, error) {
	return a.store.Get(ctx, tenantID, id)
}

func (a *invoiceWriterAdapter) MarkPaid(ctx context.Context, tenantID, id string, stripePaymentIntentID string, paidAt time.Time) (domain.Invoice, error) {
	return a.store.MarkPaid(ctx, tenantID, id, stripePaymentIntentID, paidAt)
}

func (a *invoiceWriterAdapter) CreateInvoiceWithLineItems(ctx context.Context, tenantID string, inv domain.Invoice, items []domain.InvoiceLineItem) (domain.Invoice, error) {
	return a.store.CreateWithLineItems(ctx, tenantID, inv, items)
}

func (a *invoiceWriterAdapter) SetAutoChargePending(ctx context.Context, tenantID, id string, pending bool) error {
	return a.store.SetAutoChargePending(ctx, tenantID, id, pending)
}

func (a *invoiceWriterAdapter) ListAutoChargePending(ctx context.Context, limit int) ([]domain.Invoice, error) {
	return a.store.ListAutoChargePending(ctx, limit)
}

func (a *invoiceWriterAdapter) SetTaxTransaction(ctx context.Context, tenantID, id string, taxTransactionID string) error {
	return a.store.SetTaxTransaction(ctx, tenantID, id, taxTransactionID)
}

func (a *invoiceWriterAdapter) ListLineItems(ctx context.Context, tenantID, invoiceID string) ([]domain.InvoiceLineItem, error) {
	return a.store.ListLineItems(ctx, tenantID, invoiceID)
}

func (a *invoiceWriterAdapter) ApplyDiscountAtomic(ctx context.Context, tenantID, invoiceID string, update domain.InvoiceDiscountUpdate, lineItems []domain.InvoiceLineItem) (domain.Invoice, error) {
	return a.store.ApplyDiscountAtomic(ctx, tenantID, invoiceID, update, lineItems)
}

// creditGrantAdapter bridges credit.Service → creditnote.CreditGranter.
type creditGrantAdapter struct {
	svc *credit.Service
}

func (a *creditGrantAdapter) Grant(ctx context.Context, tenantID string, input creditnote.CreditGrantInput) error {
	_, err := a.svc.Grant(ctx, tenantID, credit.GrantInput{
		CustomerID:  input.CustomerID,
		AmountCents: input.AmountCents,
		Description: input.Description,
		InvoiceID:   input.InvoiceID,
	})
	return err
}

// creditNoteListerAdapter bridges creditnote.Service → invoice.CreditNoteLister.
type creditNoteListerAdapter struct {
	svc *creditnote.Service
}

func (a *creditNoteListerAdapter) List(ctx context.Context, tenantID, invoiceID string) ([]domain.CreditNote, error) {
	return a.svc.List(ctx, creditnote.ListFilter{
		TenantID:  tenantID,
		InvoiceID: invoiceID,
	})
}

// refundIssuerAdapter bridges creditnote.Service → invoice.RefundIssuer.
// Translates the handler-facing invoice.RefundInput to the creditnote form;
// both types are near-identical by design so the handler doesn't have to
// import creditnote just to issue a refund.
type refundIssuerAdapter struct {
	svc *creditnote.Service
}

func (a *refundIssuerAdapter) IssueRefund(ctx context.Context, tenantID string, input invoice.RefundInput) (domain.CreditNote, error) {
	return a.svc.CreateRefund(ctx, tenantID, creditnote.RefundInput{
		InvoiceID:   input.InvoiceID,
		AmountCents: input.AmountCents,
		Reason:      input.Reason,
		Description: input.Description,
	})
}

// paymentRetrierAdapter bridges Stripe + invoice/customer stores → dunning.PaymentRetrier.
type paymentRetrierAdapter struct {
	charger       *payment.Stripe
	invoiceStore  *invoice.PostgresStore
	paymentSetups payment.PaymentSetupStore
}

func (a *paymentRetrierAdapter) RetryPayment(ctx context.Context, tenantID, invoiceID, customerID string) error {
	inv, err := a.invoiceStore.Get(ctx, tenantID, invoiceID)
	if err != nil {
		return fmt.Errorf("get invoice: %w", err)
	}
	if inv.AmountDueCents <= 0 {
		return nil // Nothing to charge
	}

	ps, err := a.paymentSetups.GetPaymentSetup(ctx, tenantID, customerID)
	if err != nil || ps.StripeCustomerID == "" {
		return fmt.Errorf("no payment method for customer")
	}

	// 15s bound on the Stripe leg. Scheduler ticks run tens of retries
	// back-to-back; without this, one tenant with a network-partitioned
	// Stripe call could hold the goroutine for the full request deadline
	// (minutes), starving every other tenant's retry this tick.
	chargeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	_, err = a.charger.ChargeInvoice(chargeCtx, tenantID, inv, ps.StripeCustomerID)
	// Map payment's internal "call never happened" sentinel to dunning's
	// equivalent. Keeps peer domains from importing each other and gives
	// dunning a stable signal to skip attempt_count bookkeeping.
	if errors.Is(err, payment.ErrPaymentTransient) {
		return dunning.ErrTransientSkip
	}
	return err
}

// subscriptionPauserAdapter bridges subscription.Service → dunning.SubscriptionPauser.
type subscriptionPauserAdapter struct {
	svc *subscription.Service
}

func (a *subscriptionPauserAdapter) Pause(ctx context.Context, tenantID, id string) error {
	_, err := a.svc.Pause(ctx, tenantID, id)
	return err
}

// dunningTimelineAdapter bridges dunning.Store → invoice.DunningTimelineFetcher.
type dunningTimelineAdapter struct {
	store *dunning.PostgresStore
}

func (a *dunningTimelineAdapter) ListRunsByInvoice(ctx context.Context, tenantID, invoiceID string) ([]domain.InvoiceDunningRun, error) {
	runs, _, err := a.store.ListRuns(ctx, dunning.RunListFilter{TenantID: tenantID, InvoiceID: invoiceID})
	return runs, err
}

func (a *dunningTimelineAdapter) ListEvents(ctx context.Context, tenantID, runID string) ([]domain.InvoiceDunningEvent, error) {
	return a.store.ListEvents(ctx, tenantID, runID)
}

// customerEmailFetcherAdapter bridges customer.PostgresStore → dunning.CustomerEmailFetcher + payment.CustomerEmailResolver.
type customerEmailFetcherAdapter struct {
	store *customer.PostgresStore
}

func (a *customerEmailFetcherAdapter) GetCustomerEmail(ctx context.Context, tenantID, customerID string) (string, string, error) {
	cust, err := a.store.Get(ctx, tenantID, customerID)
	if err != nil {
		return "", "", err
	}
	email := cust.Email
	name := cust.DisplayName
	// Prefer billing profile email/name if available
	if bp, err := a.store.GetBillingProfile(ctx, tenantID, customerID); err == nil {
		if bp.Email != "" {
			email = bp.Email
		}
		if bp.LegalName != "" {
			name = bp.LegalName
		}
	}
	return email, name, nil
}

// prorationCreditGranterAdapter bridges credit.Service → subscription.ProrationCreditGranter.
type prorationCreditGranterAdapter struct {
	svc *credit.Service
}

func (a *prorationCreditGranterAdapter) GrantProration(ctx context.Context, tenantID string, input subscription.ProrationGrantInput) error {
	planChangedAt := input.SourcePlanChangedAt
	_, err := a.svc.Grant(ctx, tenantID, credit.GrantInput{
		CustomerID:               input.CustomerID,
		AmountCents:              input.AmountCents,
		Description:              input.Description,
		SourceSubscriptionID:     input.SourceSubscriptionID,
		SourceSubscriptionItemID: input.SourceSubscriptionItemID,
		SourcePlanChangedAt:      &planChangedAt,
		SourceChangeType:         input.SourceChangeType,
	})
	return err
}

func (a *prorationCreditGranterAdapter) GetByProrationSource(ctx context.Context, tenantID, subscriptionID, subscriptionItemID string, changeType domain.ItemChangeType, changeAt time.Time) (domain.CreditLedgerEntry, error) {
	return a.svc.GetByProrationSource(ctx, tenantID, subscriptionID, subscriptionItemID, changeType, changeAt)
}

// prorationInvoiceCreatorAdapter bridges invoice.PostgresStore + tenant.SettingsStore → subscription.ProrationInvoiceCreator.
type prorationInvoiceCreatorAdapter struct {
	store    *invoice.PostgresStore
	numberer invoice.InvoiceNumberer
}

func (a *prorationInvoiceCreatorAdapter) CreateInvoiceWithLineItems(ctx context.Context, tenantID string, inv domain.Invoice, items []domain.InvoiceLineItem) (domain.Invoice, error) {
	return a.store.CreateWithLineItems(ctx, tenantID, inv, items)
}

func (a *prorationInvoiceCreatorAdapter) GetByProrationSource(ctx context.Context, tenantID, subscriptionID, subscriptionItemID string, changeType domain.ItemChangeType, changeAt time.Time) (domain.Invoice, error) {
	return a.store.GetByProrationSource(ctx, tenantID, subscriptionID, subscriptionItemID, changeType, changeAt)
}

func (a *prorationInvoiceCreatorAdapter) NextInvoiceNumber(ctx context.Context, tenantID string) (string, error) {
	return a.numberer.NextInvoiceNumber(ctx, tenantID)
}

// prorationTaxApplierAdapter bridges billing.Engine → subscription.ProrationTaxApplier.
// Narrow translation: same signature, different named return type so the
// subscription package doesn't import billing.
type prorationTaxApplierAdapter struct {
	engine *billing.Engine
}

func (a *prorationTaxApplierAdapter) ApplyTaxToLineItems(ctx context.Context, tenantID, customerID, currency string, subtotal, discount int64, lineItems []domain.InvoiceLineItem) (subscription.ProrationTaxResult, error) {
	r, err := a.engine.ApplyTaxToLineItems(ctx, tenantID, customerID, currency, subtotal, discount, lineItems)
	if err != nil {
		return subscription.ProrationTaxResult{}, err
	}
	return subscription.ProrationTaxResult{
		TaxAmountCents: r.TaxAmountCents,
		TaxRateBP:      r.TaxRateBP,
		TaxName:        r.TaxName,
		TaxCountry:     r.TaxCountry,
		TaxID:          r.TaxID,
		SubtotalCents:  r.SubtotalCents,
		DiscountCents:  r.DiscountCents,
	}, nil
}

// customerLookupAdapter bridges customer.PostgresStore → customerportal.CustomerLookup.
// The two EmailMatch types are structurally identical; the adapter keeps
// customerportal from importing customer (dep-graph layering).
type customerLookupAdapter struct {
	store *customer.PostgresStore
}

func (a *customerLookupAdapter) FindByEmailBlindIndex(ctx context.Context, blind string, limit int) ([]customerportal.CustomerMatch, error) {
	matches, err := a.store.FindByEmailBlindIndex(ctx, blind, limit)
	if err != nil {
		return nil, err
	}
	out := make([]customerportal.CustomerMatch, len(matches))
	for i, m := range matches {
		out[i] = customerportal.CustomerMatch{
			TenantID:   m.TenantID,
			CustomerID: m.CustomerID,
			Livemode:   m.Livemode,
		}
	}
	return out, nil
}

// bounceReporterAdapter bridges email.Sender → customer.Service. When
// SMTP rejects a send with a permanent-failure (5xx), the Sender calls
// ReportBounce with (tenantID, email, reason). This adapter resolves
// the email via the blind-index lookup and flips email_status on every
// matching customer in that tenant. Multiple matches can happen when
// two customers share an email (rare; we mark all).
//
// Lives in internal/api so email doesn't import customer — keeps the
// one-way-coupled layering intact.
type bounceReporterAdapter struct {
	blinder *crypto.Blinder
	store   *customer.PostgresStore
	svc     *customer.Service
}

func (a *bounceReporterAdapter) ReportBounce(ctx context.Context, tenantID, email, reason string) {
	if a == nil || a.store == nil || a.svc == nil || a.blinder == nil || email == "" {
		return
	}
	blind := a.blinder.Blind(email)
	if blind == "" {
		return
	}
	matches, err := a.store.FindByEmailBlindIndex(ctx, blind, 10)
	if err != nil {
		return
	}
	for _, m := range matches {
		if m.TenantID != tenantID {
			continue
		}
		if err := a.svc.MarkEmailBounced(ctx, tenantID, m.CustomerID, reason); err != nil {
			// Swallow — the sender has already logged the upstream bounce.
			// Failure here just means the badge won't update, not a
			// correctness issue.
			_ = err
		}
	}
}

// hostedInvoiceStripeAdapter bridges *payment.StripeClients →
// hostedinvoice.CheckoutSessionCreator. The caller is a public
// unauthenticated request: we resolve livemode from the invoice row itself
// (not from any request context), pick the matching Stripe key, and build
// a Checkout Session in payment mode with a single pre-totaled line item.
// Velox owns the tax computation, so Stripe's automatic_tax is
// intentionally off — the UnitAmount already includes tax from the
// invoice row.
//
// Metadata stamps velox_purpose=hosted_invoice_pay so the existing
// payment-intent webhook path can route successful charges to
// Invoice.RecordPayment via invoice_id lookup, instead of mis-identifying
// these as subscription-billing charges.
type hostedInvoiceStripeAdapter struct {
	clients *payment.StripeClients
	db      *postgres.DB
}

func (a *hostedInvoiceStripeAdapter) CreateInvoicePaymentSession(
	ctx context.Context, tenantID string, inv domain.Invoice, successURL, cancelURL string,
) (string, error) {
	if a == nil || a.clients == nil {
		return "", fmt.Errorf("stripe not configured")
	}
	// Livemode isn't part of domain.Invoice (see invoice/postgres.go
	// scanInvDest — the test-mode migration added the column but kept the
	// struct thin). Separate read keeps the adapter self-contained and
	// matches the pattern in payment/public_handler.go.
	var livemode bool
	if err := a.db.Pool.QueryRowContext(ctx,
		`SELECT livemode FROM invoices WHERE id = $1`, inv.ID).Scan(&livemode); err != nil {
		return "", fmt.Errorf("resolve livemode: %w", err)
	}
	sc := a.clients.For(ctx, tenantID, livemode)
	if sc == nil {
		return "", fmt.Errorf("stripe not configured for mode livemode=%v", livemode)
	}
	currency := strings.ToLower(inv.Currency)
	productName := "Invoice " + inv.InvoiceNumber
	// Duplicate the metadata on BOTH the session and the PaymentIntent.
	// Session-level metadata only lives on the checkout_session object;
	// Stripe does NOT automatically copy it to the underlying PaymentIntent.
	// Our webhook path routes payment_intent.succeeded → Invoice.MarkPaid
	// by reading velox_invoice_id off the PI's metadata (payment/handler.go
	// line 195), so PaymentIntentData.Metadata is the one that actually
	// matters. The session copy is kept for operator visibility when
	// inspecting a checkout session directly in Stripe dashboard.
	meta := map[string]string{
		"velox_invoice_id":  inv.ID,
		"velox_tenant_id":   tenantID,
		"velox_customer_id": inv.CustomerID,
		"velox_purpose":     "hosted_invoice_pay",
	}
	sess, err := sc.V1CheckoutSessions.Create(ctx, &stripe.CheckoutSessionCreateParams{
		Mode: stripe.String(string(stripe.CheckoutSessionModePayment)),
		LineItems: []*stripe.CheckoutSessionCreateLineItemParams{
			{
				Quantity: stripe.Int64(1),
				PriceData: &stripe.CheckoutSessionCreateLineItemPriceDataParams{
					Currency:   stripe.String(currency),
					UnitAmount: stripe.Int64(inv.AmountDueCents),
					ProductData: &stripe.CheckoutSessionCreateLineItemPriceDataProductDataParams{
						Name: stripe.String(productName),
					},
				},
			},
		},
		PaymentMethodTypes: stripe.StringSlice([]string{"card"}),
		SuccessURL:         stripe.String(successURL),
		CancelURL:          stripe.String(cancelURL),
		PaymentIntentData: &stripe.CheckoutSessionCreatePaymentIntentDataParams{
			Metadata:    meta,
			Description: stripe.String(productName),
		},
		Params: stripe.Params{
			Metadata: meta,
		},
	})
	if err != nil {
		return "", fmt.Errorf("checkout session: %w", err)
	}
	return sess.URL, nil
}
