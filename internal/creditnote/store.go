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
	// TransitionStatus is a compare-and-swap status flip: it succeeds (won=true)
	// only if the credit note is currently in `from`. Used to serialize the
	// draft→issued transition against concurrent/retried Issue() calls.
	TransitionStatus(ctx context.Context, tenantID, id string, from, to domain.CreditNoteStatus) (bool, error)
	UpdateRefundStatus(ctx context.Context, tenantID, id string, status domain.RefundStatus, stripeRefundID string) error
	// UpdateAllocation persists the three-channel allocation
	// (refund / credit / out-of-band). Used by Issue() to re-derive the
	// allocation from the current invoice state when a CN created against
	// a then-unpaid invoice is issued after that invoice became paid.
	UpdateAllocation(ctx context.Context, tenantID, id string, refundCents, creditCents, outOfBandCents int64) error
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
