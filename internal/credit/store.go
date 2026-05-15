package credit

import (
	"context"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
)

type Store interface {
	AppendEntry(ctx context.Context, tenantID string, entry domain.CreditLedgerEntry) (domain.CreditLedgerEntry, error)
	// ApplyToInvoiceAtomic debits credits and reduces invoice.amount_due
	// atomically. `at` stamps both the new ledger usage entry and the
	// invoice's updated_at, keeping the application on simulated time
	// during catchup (cycle-close instant). Pass zero in operator paths
	// to fall back to clock.Now() at the postgres layer.
	ApplyToInvoiceAtomic(ctx context.Context, tenantID, customerID, invoiceID, invoiceDesc string, invoiceAmountCents int64, at time.Time) (int64, error)

	// AdjustAtomic appends a manual adjustment entry with the balance check
	// performed inside the same locked tx. Closes the TOCTOU race where two
	// concurrent deductions each observe the current balance, each pass the
	// "balance + amount >= 0" check, and both commit — overdrafting the
	// ledger. Returns ErrInsufficientBalance if the locked balance plus the
	// amount would be negative.
	AdjustAtomic(ctx context.Context, tenantID, customerID, description string, amountCents int64) (domain.CreditLedgerEntry, error)
	GetBalance(ctx context.Context, tenantID, customerID string) (domain.CreditBalance, error)
	GetByProrationSource(ctx context.Context, tenantID, subscriptionID, subscriptionItemID string, changeType domain.ItemChangeType, changeAt time.Time) (domain.CreditLedgerEntry, error)
	ListBalances(ctx context.Context, tenantID string) ([]domain.CreditBalance, error)
	ListEntries(ctx context.Context, filter ListFilter) ([]domain.CreditLedgerEntry, error)
	ListExpiredGrants(ctx context.Context) ([]domain.CreditLedgerEntry, error)

	// ListExpiredGrantsForClock is the catchup-path counterpart to
	// ListExpiredGrants. ADR-029 Phase 4: clock-pinned customer grants
	// expire only on operator Advance (against the clock's frozen_time),
	// never on the wall-clock cron tick.
	ListExpiredGrantsForClock(ctx context.Context, tenantID, clockID string, frozenTime time.Time) ([]domain.CreditLedgerEntry, error)
}

type ListFilter struct {
	TenantID   string
	CustomerID string
	EntryType  string
	InvoiceID  string
	Limit      int
	Offset     int
	Sort       string // closed allow-list (validated in store); empty defaults to created_at
	SortDir    string // "asc" or "desc"; empty defaults to desc
}
