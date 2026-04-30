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

	// ApplyDiscountAtomic stamps a coupon discount (and the recomputed tax
	// snapshot that follows from it) onto a draft invoice in a single tx.
	// Locks the invoice FOR UPDATE, re-checks the state gates (draft,
	// discount_cents = 0, tax_transaction_id IS NULL) under the lock, then
	// rewrites discount/tax/totals. Per-line tax stamps are rewritten from
	// the supplied lineItems (keyed by id) so the recompute is authoritative.
	//
	// Returns errs.InvalidState when a gate fails (caller surfaces 409) and
	// errs.ErrNotFound when the invoice doesn't exist (caller surfaces 404).
	ApplyDiscountAtomic(ctx context.Context, tenantID, invoiceID string, update domain.InvoiceDiscountUpdate, lineItems []domain.InvoiceLineItem) (domain.Invoice, error)

	// UpdateTaxAtomic re-stamps an invoice's tax decision after a manual
	// retry. Locks the invoice row, gates on tax_status in (pending, failed)
	// and status='draft', rewrites per-line tax stamps and invoice-level tax
	// columns, increments tax_retry_count, recomputes total + amount_due.
	// Returns errs.InvalidState when a gate fails (caller surfaces 409) and
	// errs.ErrNotFound when the invoice doesn't exist (caller surfaces 404).
	UpdateTaxAtomic(ctx context.Context, tenantID, invoiceID string, update domain.InvoiceTaxRetryUpdate, lineItems []domain.InvoiceLineItem) (domain.Invoice, error)

	// HasSucceededInvoice reports whether the customer has any invoice with
	// payment_status = 'succeeded'. Backs the coupon first_time_customer_only
	// restriction — existence-only so the query can use LIMIT 1 instead of
	// paging full history.
	HasSucceededInvoice(ctx context.Context, tenantID, customerID string) (bool, error)

	// SetPublicToken writes (or overwrites) the hosted-invoice-URL token for
	// an invoice. Called from Service.Finalize at first generation and from
	// the rotate-public-token endpoint for explicit rotation. Invoice must
	// exist and be non-draft; drafts never carry a token. Returns
	// errs.ErrNotFound if the invoice is missing or still draft.
	SetPublicToken(ctx context.Context, tenantID, invoiceID, token string) error

	// GetByPublicToken resolves the hosted-invoice-URL token to its invoice.
	// Runs under TxBypass because the caller is unauthenticated: the public
	// route receives a raw token from the URL and must resolve it to a
	// tenant BEFORE any tenant context can be set. The token itself is the
	// credential (256 bits of entropy, UNIQUE indexed) so cross-tenant
	// probing isn't feasible. Returns errs.ErrNotFound on miss.
	GetByPublicToken(ctx context.Context, token string) (domain.Invoice, error)

	// GetByStripeInvoiceID resolves a Stripe invoice id (in_xxx) to its
	// imported Velox invoice row. Backs the velox-import CLI's idempotency
	// check — the partial unique index from migration 0063 makes
	// stripe_invoice_id the dedup key for imported rows. Returns
	// errs.ErrNotFound when no invoice carries that Stripe id.
	GetByStripeInvoiceID(ctx context.Context, tenantID, stripeInvoiceID string) (domain.Invoice, error)
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
