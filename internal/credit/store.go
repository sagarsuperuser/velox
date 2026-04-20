package credit

import (
	"context"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
)

type Store interface {
	AppendEntry(ctx context.Context, tenantID string, entry domain.CreditLedgerEntry) (domain.CreditLedgerEntry, error)
	ApplyToInvoiceAtomic(ctx context.Context, tenantID, customerID, invoiceID, invoiceDesc string, invoiceAmountCents int64) (int64, error)

	// AdjustAtomic appends a manual adjustment entry with the balance check
	// performed inside the same locked tx. Closes the TOCTOU race where two
	// concurrent deductions each observe the current balance, each pass the
	// "balance + amount >= 0" check, and both commit — overdrafting the
	// ledger. Returns ErrInsufficientBalance if the locked balance plus the
	// amount would be negative.
	AdjustAtomic(ctx context.Context, tenantID, customerID, description string, amountCents int64) (domain.CreditLedgerEntry, error)
	GetBalance(ctx context.Context, tenantID, customerID string) (domain.CreditBalance, error)
	GetByProrationSource(ctx context.Context, tenantID, subscriptionID, subscriptionItemID string, planChangedAt time.Time) (domain.CreditLedgerEntry, error)
	ListBalances(ctx context.Context, tenantID string) ([]domain.CreditBalance, error)
	ListEntries(ctx context.Context, filter ListFilter) ([]domain.CreditLedgerEntry, error)
	ListExpiredGrants(ctx context.Context) ([]domain.CreditLedgerEntry, error)
}

type ListFilter struct {
	TenantID   string
	CustomerID string
	EntryType  string
	InvoiceID  string
	Limit      int
	Offset     int
}
