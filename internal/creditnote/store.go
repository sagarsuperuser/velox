package creditnote

import (
	"context"

	"github.com/sagarsuperuser/velox/internal/domain"
)

type Store interface {
	Create(ctx context.Context, tenantID string, cn domain.CreditNote) (domain.CreditNote, error)
	Get(ctx context.Context, tenantID, id string) (domain.CreditNote, error)
	List(ctx context.Context, filter ListFilter) ([]domain.CreditNote, error)
	UpdateStatus(ctx context.Context, tenantID, id string, status domain.CreditNoteStatus) (domain.CreditNote, error)
	UpdateRefundStatus(ctx context.Context, tenantID, id string, status domain.RefundStatus, stripeRefundID string) error
	// SetTaxTransaction persists the reversal transaction id returned by
	// the tax provider at Issue time.
	SetTaxTransaction(ctx context.Context, tenantID, id string, taxTransactionID string) error
	CreateLineItem(ctx context.Context, tenantID string, item domain.CreditNoteLineItem) (domain.CreditNoteLineItem, error)
	ListLineItems(ctx context.Context, tenantID, creditNoteID string) ([]domain.CreditNoteLineItem, error)
}

type ListFilter struct {
	TenantID   string
	InvoiceID  string
	CustomerID string
	Status     string
	Limit      int
	Offset     int
}
