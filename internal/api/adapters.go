package api

import (
	"context"

	"github.com/sagarsuperuser/velox/internal/credit"
	"github.com/sagarsuperuser/velox/internal/creditnote"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/invoice"
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
