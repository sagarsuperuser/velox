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
	// MarkPaid flips status='paid', payment_status='succeeded',
	// amount_paid=amount_due, amount_due=0 in one transaction. Used by
	// the engine's zero-amount auto-pay path, the billing threshold
	// scan, and the operator-driven Record Offline Payment flow. The
	// store is the right place to atomically mutate all four fields
	// — Service.RecordOfflinePayment calls this so an out-of-band
	// collection produces the same invoice end-state as a successful
	// engine charge.
	MarkPaid(ctx context.Context, tenantID, id string, stripePaymentIntentID string, paidAt time.Time) (domain.Invoice, error)
	ApplyCreditNote(ctx context.Context, tenantID, id string, amountCents int64) (domain.Invoice, error)
	ApplyCredits(ctx context.Context, tenantID, id string, amountCents int64) (domain.Invoice, error)

	UpdateTotals(ctx context.Context, tenantID, id string, subtotal, total, amountDue int64) (domain.Invoice, error)
	ListApproachingDue(ctx context.Context, daysBeforeDue int) ([]domain.Invoice, error)

	// ListApproachingDueForClock is the catchup-path counterpart.
	// ADR-029 Phase 6: clock-pinned reminder candidates use the
	// clock's frozen_time as the "now" anchor, not wall-clock.
	ListApproachingDueForClock(ctx context.Context, tenantID, clockID string, frozenTime time.Time, daysBeforeDue int) ([]domain.Invoice, error)

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

	// ListPendingTaxRetry returns draft invoices awaiting another
	// tax-calculation attempt: tax_status in (pending, failed),
	// tax_error_code is in the retryable set (the worker filters
	// further on this side), tax_next_retry_at is NULL or in the
	// past, and tax_retry_count is below the per-invoice cap.
	// Ordered by tax_next_retry_at ASC (NULLS FIRST) so newly-
	// stuck invoices process first. Cross-tenant; returned rows
	// carry their tenant_id so the caller can dispatch per-row
	// with the right RLS partition.
	//
	// livemode filters to a single mode partition. The reconciler
	// runs once per mode in the scheduler tick; without this filter
	// the cross-mode RLS-bypassed scan would return rows for the
	// other mode that fail per-row RLS lookup with "not found".
	ListPendingTaxRetry(ctx context.Context, batch int, retryableCodes []string, maxAttempts int, livemode bool) ([]domain.Invoice, error)

	// ListPendingTaxRetryForClock is the catchup-path counterpart to
	// ListPendingTaxRetry — returns clock-pinned draft invoices stuck
	// at tax_status=pending with a retryable code. ADR-029 Phase 2:
	// clock-pinned tax retries fire only on operator Advance, never on
	// the wall-clock tick. One retry per row per Advance (no backoff
	// gate) — operator-friendly: each click does something visible.
	ListPendingTaxRetryForClock(ctx context.Context, tenantID, clockID string, retryableCodes []string, maxAttempts, limit int) ([]domain.Invoice, error)

	// ListCustomerDataInvalidErrors returns draft invoices for ONE
	// customer stuck on `customer_data_invalid` — the only tax error a
	// billing-profile update can resolve. Backs the per-customer
	// retry-flush fired from customer.Service.UpsertBillingProfile,
	// mirroring the ADR-019 Stripe-reconnect flush pattern at
	// per-customer scope.
	ListCustomerDataInvalidErrors(ctx context.Context, tenantID, customerID string) ([]domain.Invoice, error)

	// ListProviderConfigErrors returns draft invoices stuck on Stripe-
	// configuration errors (tax_error_code IN provider_not_configured,
	// provider_auth) for the given (tenant, livemode). Used by the
	// tenantstripe.Connect path to fan out a one-shot retry the moment
	// fresh credentials land — operator-driven recovery, not
	// background polling. Tenant-scoped RLS via TxTenant; the per-mode
	// filter is in the WHERE clause so a test-mode connect doesn't
	// surface live-mode rows. ADR-019.
	ListProviderConfigErrors(ctx context.Context, tenantID string, livemode bool) ([]domain.Invoice, error)

	// HasSucceededInvoice reports whether the customer has any invoice with
	// payment_status = 'succeeded'. Backs the coupon first_time_customer_only
	// restriction — existence-only so the query can use LIMIT 1 instead of
	// paging full history.
	HasSucceededInvoice(ctx context.Context, tenantID, customerID string) (bool, error)

	// GetOutstandingBalance returns the customer's accounts-receivable
	// exposure: SUM(amount_due_cents) + COUNT(*) of finalized invoices
	// in payment_status pending/failed/unknown, excluding voided +
	// uncollectible (those are either canceled or written off as bad
	// debt). Powers the "Outstanding balance" card on customer detail —
	// industry-standard surface (Stripe customer overview tile, Lago /
	// Chargebee / Recurly customer page) that operators rely on to see
	// total AR exposure for a customer at a glance.
	GetOutstandingBalance(ctx context.Context, tenantID, customerID string) (OutstandingBalance, error)

	// SetPublicToken writes (or overwrites) the hosted-invoice-URL token for
	// an invoice. Called from Service.Finalize at first generation and from
	// the rotate-public-token endpoint for explicit rotation. Invoice must
	// exist and be non-draft; drafts never carry a token. Returns
	// errs.ErrNotFound if the invoice is missing or still draft.
	SetPublicToken(ctx context.Context, tenantID, invoiceID, token string) error

	// GetByPublicToken resolves the hosted-invoice-URL token to its invoice
	// AND its livemode (the second return value). Runs under TxBypass because
	// the caller is unauthenticated: the public route receives a raw token
	// from the URL and must resolve it to a tenant + mode BEFORE any tenant
	// context can be set. The livemode return is what callers need to pin
	// `postgres.WithLivemode(ctx, …)` on the request context for every
	// downstream RLS-scoped read; without it, the public route defaults to
	// live and a test-mode invoice's line items / customer / settings reads
	// silently 404. The token itself is the credential (256 bits of entropy,
	// UNIQUE indexed) so cross-tenant probing isn't feasible. Returns
	// errs.ErrNotFound on miss.
	GetByPublicToken(ctx context.Context, token string) (domain.Invoice, bool, error)

	// GetByStripeInvoiceID resolves a Stripe invoice id (in_xxx) to its
	// imported Velox invoice row. Backs the velox-import CLI's idempotency
	// check — the partial unique index from migration 0063 makes
	// stripe_invoice_id the dedup key for imported rows. Returns
	// errs.ErrNotFound when no invoice carries that Stripe id.
	GetByStripeInvoiceID(ctx context.Context, tenantID, stripeInvoiceID string) (domain.Invoice, error)

	// FindBaseInvoiceForPeriod returns the most-recent NON-VOIDED invoice
	// carrying a base-fee line item with billing_period_start = the given
	// periodStart for the given subscription. Used by proration-credit
	// paths (BillOnCancel + handleItemProration on downgrade) to verify
	// the customer actually paid for the current period before granting
	// a "refund" — industry standard (Chargebee distinguishes Refundable
	// vs Adjustment credit notes based on this; Stripe warns operators
	// to disable proration when the latest invoice is unpaid).
	//
	// For in_advance plans, the cycle-close invoice from the prior period
	// (or BillOnCreate's day-1 invoice for new subs) carries the base
	// line whose billing_period_start matches the now-being-canceled
	// period's start. For in_arrears plans no such line exists —
	// callers shouldn't be granting unpaid-period credits to in_arrears
	// subs in the first place. Returns errs.ErrNotFound when no
	// matching invoice exists.
	FindBaseInvoiceForPeriod(ctx context.Context, tenantID, subscriptionID string, periodStart time.Time) (domain.Invoice, error)
}

// OutstandingBalance is the customer-AR snapshot — total cents owed
// across all unpaid (pending / failed / unknown) finalized invoices,
// excluding voided + uncollectible. UnpaidCount is the cardinality
// of those invoices so the UI can show "$X across N unpaid invoices."
type OutstandingBalance struct {
	TotalCents  int64 `json:"total_cents"`
	UnpaidCount int   `json:"unpaid_count"`
}

type ListFilter struct {
	TenantID       string
	CustomerID     string
	SubscriptionID string
	Status         string
	PaymentStatus  string
	// IDs scopes the result to a specific set of invoice IDs. Used by
	// other list pages (CreditNotes) to fetch exactly the invoices
	// referenced by their primary rows — avoids the
	// "list-then-client-side-join" pagination bug. Empty = no filter.
	IDs    []string
	Limit  int
	Offset int
	// Sort: column name from a closed set (validated by the store).
	// Empty string means default (created_at).
	Sort string
	// SortDir: "asc" or "desc". Empty string means desc.
	SortDir string
}
