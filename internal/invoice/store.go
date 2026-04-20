package invoice

import (
	"context"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
)

type Store interface {
	Create(ctx context.Context, tenantID string, inv domain.Invoice) (domain.Invoice, error)
	CreateWithLineItems(ctx context.Context, tenantID string, inv domain.Invoice, items []domain.InvoiceLineItem) (domain.Invoice, error)
	Get(ctx context.Context, tenantID, id string) (domain.Invoice, error)
	GetByNumber(ctx context.Context, tenantID, number string) (domain.Invoice, error)
	GetByProrationSource(ctx context.Context, tenantID, subscriptionID, subscriptionItemID string, changeType domain.ItemChangeType, changeAt time.Time) (domain.Invoice, error)
	List(ctx context.Context, filter ListFilter) ([]domain.Invoice, int, error)
	UpdateStatus(ctx context.Context, tenantID, id string, status domain.InvoiceStatus) (domain.Invoice, error)
	UpdatePayment(ctx context.Context, tenantID, id string, paymentStatus domain.InvoicePaymentStatus, stripePaymentIntentID, lastPaymentError string, paidAt *time.Time) (domain.Invoice, error)
	ApplyCreditNote(ctx context.Context, tenantID, id string, amountCents int64) (domain.Invoice, error)
	ApplyCredits(ctx context.Context, tenantID, id string, amountCents int64) (domain.Invoice, error)

	UpdateTotals(ctx context.Context, tenantID, id string, subtotal, total, amountDue int64) (domain.Invoice, error)
	ListApproachingDue(ctx context.Context, daysBeforeDue int) ([]domain.Invoice, error)

	SetAutoChargePending(ctx context.Context, tenantID, id string, pending bool) error
	ListAutoChargePending(ctx context.Context, limit int) ([]domain.Invoice, error)

	CreateLineItem(ctx context.Context, tenantID string, item domain.InvoiceLineItem) (domain.InvoiceLineItem, error)
	ListLineItems(ctx context.Context, tenantID, invoiceID string) ([]domain.InvoiceLineItem, error)

	// AddLineItemAtomic inserts a line item and recomputes the invoice totals
	// in one transaction. It locks the invoice row FOR UPDATE, verifies draft
	// status, inserts the line item (filling InvoiceID, TenantID, and Currency
	// from the locked invoice), recomputes the subtotal from all line items,
	// and rewrites subtotal/total/amount_due. The lock prevents lost updates
	// when two clients append lines concurrently. Returns the inserted line
	// item and the updated invoice.
	AddLineItemAtomic(ctx context.Context, tenantID, invoiceID string, item domain.InvoiceLineItem) (domain.InvoiceLineItem, domain.Invoice, error)
}

type ListFilter struct {
	TenantID       string
	CustomerID     string
	SubscriptionID string
	Status         string
	PaymentStatus  string
	Limit          int
	Offset         int
}
