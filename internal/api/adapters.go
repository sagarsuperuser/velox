package api

import (
	"context"
	"fmt"
	"time"

	"github.com/sagarsuperuser/velox/internal/billing"
	"github.com/sagarsuperuser/velox/internal/credit"
	"github.com/sagarsuperuser/velox/internal/creditnote"
	"github.com/sagarsuperuser/velox/internal/customer"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/dunning"
	"github.com/sagarsuperuser/velox/internal/invoice"
	"github.com/sagarsuperuser/velox/internal/payment"
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

	_, err = a.charger.ChargeInvoice(ctx, tenantID, inv, ps.StripeCustomerID)
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
		CustomerID:           input.CustomerID,
		AmountCents:          input.AmountCents,
		Description:          input.Description,
		SourceSubscriptionID: input.SourceSubscriptionID,
		SourcePlanChangedAt:  &planChangedAt,
	})
	return err
}

func (a *prorationCreditGranterAdapter) GetByProrationSource(ctx context.Context, tenantID, subscriptionID string, planChangedAt time.Time) (domain.CreditLedgerEntry, error) {
	return a.svc.GetByProrationSource(ctx, tenantID, subscriptionID, planChangedAt)
}

// prorationInvoiceCreatorAdapter bridges invoice.PostgresStore + tenant.SettingsStore → subscription.ProrationInvoiceCreator.
type prorationInvoiceCreatorAdapter struct {
	store    *invoice.PostgresStore
	numberer invoice.InvoiceNumberer
}

func (a *prorationInvoiceCreatorAdapter) CreateInvoiceWithLineItems(ctx context.Context, tenantID string, inv domain.Invoice, items []domain.InvoiceLineItem) (domain.Invoice, error) {
	return a.store.CreateWithLineItems(ctx, tenantID, inv, items)
}

func (a *prorationInvoiceCreatorAdapter) GetByProrationSource(ctx context.Context, tenantID, subscriptionID string, planChangedAt time.Time) (domain.Invoice, error) {
	return a.store.GetByProrationSource(ctx, tenantID, subscriptionID, planChangedAt)
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
