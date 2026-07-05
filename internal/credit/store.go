package credit

import (
	"context"
	"database/sql"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
)

type Store interface {
	AppendEntry(ctx context.Context, tenantID string, entry domain.CreditLedgerEntry) (domain.CreditLedgerEntry, error)
	// ListGrantSummaries returns the customer's positive blocks with
	// per-block remaining (amount - consumed) — the commit/promo burndown
	// rows. includeExhausted=false returns live blocks only.
	ListGrantSummaries(ctx context.Context, tenantID, customerID string, includeExhausted bool) ([]GrantSummary, error)
	// AppendEntryTx is the in-transaction variant used by callers that
	// need to compose a credit-ledger insert atomically with other
	// writes (e.g. the subscription handler's AddItem-with-proration
	// flow inserts the sub item + the credit entry in one tx). The
	// caller owns the tx; the store just executes inside it.
	AppendEntryTx(ctx context.Context, tx *sql.Tx, tenantID string, entry domain.CreditLedgerEntry) (domain.CreditLedgerEntry, error)
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
	// GetByCreditNoteSource fetches the grant row created by a specific
	// credit-note Issue(). Used by GrantForCreditNote to recover from
	// ErrAlreadyExists on retry (the partial unique index from migration
	// 0093 enforces one grant per (tenant, CN)).
	GetByCreditNoteSource(ctx context.Context, tenantID, creditNoteID string) (domain.CreditLedgerEntry, error)
	ListBalances(ctx context.Context, tenantID string) ([]domain.CreditBalance, error)
	ListEntries(ctx context.Context, filter ListFilter) ([]domain.CreditLedgerEntry, error)

	// ExpireGrantAtomic retires one expired grant — flips its
	// consumed_cents to amount_cents and appends the -remaining expiry
	// entry in a SINGLE transaction, recomputing `remaining` under the
	// same row lock the apply/adjust paths hold. Returns the expired
	// cents; 0 with nil error when the grant was already fully consumed
	// or retired by the time the lock was acquired (clean no-op — the
	// caller's candidate snapshot is expected to go stale). The
	// consumed_cents flip is the exactly-once gate: replayed and
	// concurrent sweeps converge on one expiry entry per grant.
	ExpireGrantAtomic(ctx context.Context, tenantID, customerID, grantID string) (int64, error)
	ListExpiredGrants(ctx context.Context) ([]domain.CreditLedgerEntry, error)

	// ListExpiredGrantsForClock is the catchup-path counterpart to
	// ListExpiredGrants. ADR-029 Phase 4: clock-pinned customer grants
	// expire only on operator Advance (against the clock's frozen_time),
	// never on the wall-clock cron tick.
	ListExpiredGrantsForClock(ctx context.Context, tenantID, clockID string, frozenTime time.Time) ([]domain.CreditLedgerEntry, error)

	// RetireCommitGrantForInvoiceTx retires the remaining balance of the
	// commit grant funded by the given invoice, on the caller's tx — the
	// void leg of ADR-078. Clean no-op (0, nil) when the invoice funded no
	// grant or the grant is already fully consumed/retired. The
	// consumed_cents CAS flip is the structural exactly-once gate, so the
	// legal uncollectible→void sequence and retries converge on one
	// retirement entry.
	RetireCommitGrantForInvoiceTx(ctx context.Context, tx *sql.Tx, tenantID, invoiceID string) (int64, error)
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
