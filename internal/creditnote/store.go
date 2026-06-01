package creditnote

import (
	"context"

	"github.com/sagarsuperuser/velox/internal/domain"
)

type Store interface {
	Create(ctx context.Context, tenantID string, cn domain.CreditNote) (domain.CreditNote, error)
	// CreateUnderInvoiceLock serializes credit-note creation for one invoice:
	// it takes a per-invoice advisory lock, lists the invoice's existing credit
	// notes in the same transaction, lets `build` run the cap checks against
	// that locked snapshot and return the note to insert, and inserts it
	// atomically — closing the over-credit TOCTOU on concurrent Create.
	CreateUnderInvoiceLock(ctx context.Context, tenantID, invoiceID string, build func(existing []domain.CreditNote) (domain.CreditNote, error)) (domain.CreditNote, error)
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
	Sort       string // closed allow-list (validated in store); empty defaults to created_at
	SortDir    string // "asc" or "desc"; empty defaults to desc
}
