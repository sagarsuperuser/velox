package api

import (
	"context"
	"fmt"

	"github.com/sagarsuperuser/velox/internal/credit"
	"github.com/sagarsuperuser/velox/internal/creditnote"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/invoice"
	"github.com/sagarsuperuser/velox/internal/payment"
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
	return a.store.ApplyCreditNote(ctx, tenantID, id, amountCents)
}

func (a *invoiceWriterAdapter) GetInvoice(ctx context.Context, tenantID, id string) (domain.Invoice, error) {
	return a.store.Get(ctx, tenantID, id)
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
