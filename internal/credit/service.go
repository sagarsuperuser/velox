package credit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/clock"
)

type Service struct {
	store    Store
	resolver clock.Resolver
}

func NewService(store Store) *Service {
	return &Service{store: store}
}

// SetResolver wires the unified clock.Resolver. Customer-scoped credit
// mutations (Grant, ApplyToInvoice, etc.) bind effective-now from the
// customer pin so the ledger entry's created_at lands in simulated
// time on clock-pinned customers — same architectural rule as
// subscription / invoice / dunning.
func (s *Service) SetResolver(r clock.Resolver) {
	s.resolver = r
}

func (s *Service) bindForCustomer(ctx context.Context, tenantID, customerID string) context.Context {
	bound, _ := clock.BindEffectiveNow(ctx, s.resolver, clock.Pin{TenantID: tenantID, CustomerID: customerID})
	return bound
}

type GrantInput struct {
	CustomerID  string     `json:"customer_id"`
	AmountCents int64      `json:"amount_cents"`
	Description string     `json:"description"`
	InvoiceID   string     `json:"invoice_id,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`

	// SourceSubscriptionID + SourceSubscriptionItemID + SourcePlanChangedAt +
	// SourceChangeType, when all set, mark this grant as a proration credit
	// for a specific item mutation (plan downgrade, qty reduction, remove).
	// The store enforces uniqueness on the full tuple so retries return
	// ErrAlreadyExists instead of duplicating the credit — see migration 0027
	// for the unique partial index.
	SourceSubscriptionID     string                `json:"source_subscription_id,omitempty"`
	SourceSubscriptionItemID string                `json:"source_subscription_item_id,omitempty"`
	SourcePlanChangedAt      *time.Time            `json:"source_plan_changed_at,omitempty"`
	SourceChangeType         domain.ItemChangeType `json:"source_change_type,omitempty"`

	// SourceCreditNoteID dedups grants created by credit-note Issue()
	// — a retry after partial-failure hits the unique partial index
	// (migration 0093) and the store returns ErrAlreadyExists. Service
	// callers use GrantOrFetch (below) to handle the retry path.
	SourceCreditNoteID string `json:"source_credit_note_id,omitempty"`

	// At is the simulated instant the grant was earned (cancel time
	// for cancel-proration credits, plan-change time for plan-change
	// credits, operator action time for manual grants). Empty falls
	// back to clock.Now(ctx) at the postgres layer — fine for
	// operator-action paths in wall-clock. Set by engine callers
	// during catchup to keep ledger entries on simulated-time so
	// the customer's Credits tab shows per-fact chronology instead
	// of every entry stacked at advance-end frozen_time.
	At time.Time `json:"-"`
}

func (s *Service) Grant(ctx context.Context, tenantID string, input GrantInput) (domain.CreditLedgerEntry, error) {
	if input.CustomerID == "" {
		return domain.CreditLedgerEntry{}, errs.Required("customer_id")
	}
	ctx = s.bindForCustomer(ctx, tenantID, input.CustomerID)
	if input.AmountCents <= 0 {
		return domain.CreditLedgerEntry{}, errs.Invalid("amount_cents", "must be greater than 0")
	}
	if input.AmountCents > 100_000_000 { // $1M cap
		return domain.CreditLedgerEntry{}, errs.Invalid("amount_cents", "cannot exceed 1,000,000")
	}
	desc := strings.TrimSpace(input.Description)
	if desc == "" {
		return domain.CreditLedgerEntry{}, errs.Required("description")
	}
	if len(desc) > 500 {
		return domain.CreditLedgerEntry{}, errs.Invalid("description", "must be at most 500 characters")
	}
	// Reject past expires_at — a grant that's already expired at
	// creation time is dead-on-arrival: it briefly inflates the
	// balance, then the next expiry catchup retires it (with my
	// fullCycleDays/consumed-aware fixes, net effect is $0). But the
	// operator clearly didn't intend this — they likely picked the
	// wrong year/date. Industry parity: Stripe credit-grant API
	// returns 422 on past expires_at.
	//
	// Compare against clock.Now(ctx) so clock-pinned customers
	// (bindForCustomer above seeded ctx with the sim clock) evaluate
	// against simulated time, not wall-clock. Internal engine callers
	// (BillOnCancel / BillOnPlanSwapImmediate / CN Issue) don't set
	// ExpiresAt on their grants, so this gate doesn't affect refund
	// flows — only operator-driven and SDK-driven Grant calls.
	if input.ExpiresAt != nil && !input.ExpiresAt.After(clock.Now(ctx)) {
		return domain.CreditLedgerEntry{}, errs.Invalid("expires_at",
			"must be in the future — a grant that expires at or before now is dead on arrival")
	}

	return s.store.AppendEntry(ctx, tenantID, domain.CreditLedgerEntry{
		CustomerID:               input.CustomerID,
		EntryType:                domain.CreditGrant,
		AmountCents:              input.AmountCents,
		Description:              desc,
		InvoiceID:                input.InvoiceID,
		ExpiresAt:                input.ExpiresAt,
		SourceSubscriptionID:     input.SourceSubscriptionID,
		SourceSubscriptionItemID: input.SourceSubscriptionItemID,
		SourcePlanChangedAt:      input.SourcePlanChangedAt,
		SourceChangeType:         input.SourceChangeType,
		SourceCreditNoteID:       input.SourceCreditNoteID,
		CreatedAt:                input.At,
	})
}

// GrantTx is the in-transaction variant used by the subscription
// handler's atomic AddItem-with-proration flow. The caller owns the
// tx; this method runs the same validation as Grant but writes the
// ledger entry via the store's AppendEntryTx so it shares the caller's
// tx. ADR-030 atomic-proration follow-through (2026-05-29).
//
// Note: skips the bindForCustomer call (the caller is expected to have
// already bound effective-now from the affected entity's pin via the
// handler's resolver — passing the bound ctx through to us).
func (s *Service) GrantTx(ctx context.Context, tx *sql.Tx, tenantID string, input GrantInput) (domain.CreditLedgerEntry, error) {
	if input.CustomerID == "" {
		return domain.CreditLedgerEntry{}, errs.Required("customer_id")
	}
	if input.AmountCents <= 0 {
		return domain.CreditLedgerEntry{}, errs.Invalid("amount_cents", "must be greater than 0")
	}
	if input.AmountCents > 100_000_000 {
		return domain.CreditLedgerEntry{}, errs.Invalid("amount_cents", "cannot exceed 1,000,000")
	}
	desc := strings.TrimSpace(input.Description)
	if desc == "" {
		return domain.CreditLedgerEntry{}, errs.Required("description")
	}
	if len(desc) > 500 {
		return domain.CreditLedgerEntry{}, errs.Invalid("description", "must be at most 500 characters")
	}
	if input.ExpiresAt != nil && !input.ExpiresAt.After(clock.Now(ctx)) {
		return domain.CreditLedgerEntry{}, errs.Invalid("expires_at",
			"must be in the future — a grant that expires at or before now is dead on arrival")
	}
	return s.store.AppendEntryTx(ctx, tx, tenantID, domain.CreditLedgerEntry{
		CustomerID:               input.CustomerID,
		EntryType:                domain.CreditGrant,
		AmountCents:              input.AmountCents,
		Description:              desc,
		InvoiceID:                input.InvoiceID,
		ExpiresAt:                input.ExpiresAt,
		SourceSubscriptionID:     input.SourceSubscriptionID,
		SourceSubscriptionItemID: input.SourceSubscriptionItemID,
		SourcePlanChangedAt:      input.SourcePlanChangedAt,
		SourceChangeType:         input.SourceChangeType,
		SourceCreditNoteID:       input.SourceCreditNoteID,
		CreatedAt:                input.At,
	})
}

// GrantForCreditNote is the retry-safe Grant variant used by
// credit-note Issue(). Sets SourceCreditNoteID so the partial unique
// index (migration 0093) dedups retries. On ErrAlreadyExists the
// existing grant is fetched and returned — caller continues without
// double-crediting.
func (s *Service) GrantForCreditNote(ctx context.Context, tenantID, creditNoteID string, input GrantInput) (domain.CreditLedgerEntry, error) {
	if creditNoteID == "" {
		return domain.CreditLedgerEntry{}, errs.Required("credit_note_id")
	}
	input.SourceCreditNoteID = creditNoteID
	entry, err := s.Grant(ctx, tenantID, input)
	if errors.Is(err, errs.ErrAlreadyExists) {
		existing, fetchErr := s.store.GetByCreditNoteSource(ctx, tenantID, creditNoteID)
		if fetchErr != nil {
			return domain.CreditLedgerEntry{}, fmt.Errorf("dedup hit but fetch failed: %w", fetchErr)
		}
		return existing, nil
	}
	return entry, err
}

// GetByProrationSource exposes the store-level source lookup. Used by the
// subscription proration path to complete an idempotent retry after
// AppendEntry returns ErrAlreadyExists.
func (s *Service) GetByProrationSource(ctx context.Context, tenantID, subscriptionID, subscriptionItemID string, changeType domain.ItemChangeType, changeAt time.Time) (domain.CreditLedgerEntry, error) {
	return s.store.GetByProrationSource(ctx, tenantID, subscriptionID, subscriptionItemID, changeType, changeAt)
}

// ApplyToInvoice deducts credits from a customer's balance AND reduces the
// invoice's amount_due_cents in a single atomic transaction. Either both
// happen or neither does — there is no window where the ledger is debited
// but the invoice still shows the pre-credit amount due (which would cause
// double-billing when the PaymentIntent charges for the full original total).
//
// Returns the amount deducted. If balance is 0 or invoice amount is 0,
// returns 0 without any writes.
func (s *Service) ApplyToInvoice(ctx context.Context, tenantID, customerID, invoiceID string, invoiceAmountCents int64, invoiceNumber ...string) (int64, error) {
	return s.ApplyToInvoiceAt(ctx, tenantID, customerID, invoiceID, invoiceAmountCents, time.Time{}, invoiceNumber...)
}

// ApplyToInvoiceAt is the simulated-time-aware variant: `at` stamps the
// ledger usage entry and the invoice's updated_at, anchoring on the
// invoice's cycle-close instant during catchup so credits applied across
// multiple periods don't all stack at advance-end frozen_time on the
// customer's Credits tab. Pass zero from operator paths to fall back to
// clock.Now(ctx) at the postgres layer.
func (s *Service) ApplyToInvoiceAt(ctx context.Context, tenantID, customerID, invoiceID string, invoiceAmountCents int64, at time.Time, invoiceNumber ...string) (int64, error) {
	desc := fmt.Sprintf("Applied to invoice %s", invoiceID)
	if len(invoiceNumber) > 0 && invoiceNumber[0] != "" {
		desc = fmt.Sprintf("Applied to invoice %s", invoiceNumber[0])
	}
	return s.store.ApplyToInvoiceAtomic(ctx, tenantID, customerID, invoiceID, desc, invoiceAmountCents, at)
}

func (s *Service) ReverseForInvoice(ctx context.Context, tenantID, customerID, invoiceID, invoiceNumber string) (int64, error) {
	return s.reverseForInvoice(ctx, tenantID, customerID, invoiceID, invoiceNumber,
		func(entry domain.CreditLedgerEntry) error {
			_, err := s.store.AppendEntry(ctx, tenantID, entry)
			return err
		})
}

// ReverseForInvoiceTx is the in-transaction variant: the reversal grant is
// appended on the caller's *sql.Tx so it commits (or rolls back) atomically
// with the invoice status flip that drives the void. invoice.Service.Void
// threads its coordinator tx through here — a reversal failure rolls the void
// back, so the invoice never lands voided with the customer's applied credits
// still consumed. The reading of usage entries runs on its own conn (those
// rows were committed when the credit was applied); only the grant INSERT
// rides the tx, and the 0106 dedup index keeps it exactly-once.
func (s *Service) ReverseForInvoiceTx(ctx context.Context, tx *sql.Tx, tenantID, customerID, invoiceID, invoiceNumber string) (int64, error) {
	return s.reverseForInvoice(ctx, tenantID, customerID, invoiceID, invoiceNumber,
		func(entry domain.CreditLedgerEntry) error {
			_, err := s.store.AppendEntryTx(ctx, tx, tenantID, entry)
			return err
		})
}

// reverseForInvoice is the shared body: sum the usage entries applied to the
// invoice and append a matching grant via the supplied appender (own tx vs
// caller's tx). bindForCustomer stamps the grant in simulated time when the
// customer is clock-pinned.
func (s *Service) reverseForInvoice(ctx context.Context, tenantID, customerID, invoiceID, invoiceNumber string, appendFn func(domain.CreditLedgerEntry) error) (int64, error) {
	// Bind ctx clock to the customer pin so the reversal grant entry's
	// created_at stamps in simulated time when called from the void /
	// dunning-writeoff flow on a clock-pinned customer.
	ctx = s.bindForCustomer(ctx, tenantID, customerID)
	entries, err := s.store.ListEntries(ctx, ListFilter{
		TenantID:   tenantID,
		CustomerID: customerID,
		InvoiceID:  invoiceID,
		EntryType:  string(domain.CreditUsage),
		Limit:      100,
	})
	if err != nil {
		return 0, err
	}

	// Sum all usage entries for this invoice (they're negative)
	var totalUsed int64
	for _, e := range entries {
		totalUsed += -e.AmountCents // Convert negative to positive
	}

	if totalUsed <= 0 {
		return 0, nil // No credits were applied to this invoice
	}

	desc := fmt.Sprintf("Reversed — invoice %s voided", invoiceNumber)
	if invoiceNumber == "" {
		desc = fmt.Sprintf("Reversed — invoice %s voided", invoiceID)
	}

	err = appendFn(domain.CreditLedgerEntry{
		CustomerID:              customerID,
		EntryType:               domain.CreditGrant,
		AmountCents:             totalUsed,
		Description:             desc,
		InvoiceID:               invoiceID,
		SourceInvoiceReversalID: invoiceID,
	})
	if err != nil {
		// A second void / dunning manual-resolve of the same invoice hits the
		// partial unique index (migration 0106) and the store returns
		// ErrAlreadyExists with this code — the reversal already happened, so
		// this is an idempotent no-op rather than a double-credit.
		if errs.Code(err) == "credit_reversal_source_taken" {
			return 0, nil
		}
		return 0, err
	}

	return totalUsed, nil
}

// ExpireCredits finds unexpired grant entries past their expiry date and creates
// negative (expiry) entries to zero them out. Returns the count of expired grants
// and any errors encountered during processing.
func (s *Service) ExpireCredits(ctx context.Context) (int, []error) {
	grants, err := s.store.ListExpiredGrants(ctx)
	if err != nil {
		return 0, []error{fmt.Errorf("list expired grants: %w", err)}
	}
	return s.processExpiry(ctx, grants)
}

// ExpireCreditsForClock is the catchup-path counterpart. ADR-029
// Phase 4: clock-pinned customer grants expire only on operator
// Advance, against the clock's frozen_time, never on the wall-clock
// cron tick.
func (s *Service) ExpireCreditsForClock(ctx context.Context, tenantID, clockID string, frozenTime time.Time) (int, []error) {
	grants, err := s.store.ListExpiredGrantsForClock(ctx, tenantID, clockID, frozenTime)
	if err != nil {
		return 0, []error{fmt.Errorf("list expired grants for clock %s: %w", clockID, err)}
	}
	return s.processExpiry(ctx, grants)
}

// processExpiry is the shared per-grant body of ExpireCredits and
// ExpireCreditsForClock — the candidate list shape differs by trigger
// path; the per-grant ledger-append is identical.
//
// Orb's credit-block model: each grant carries consumed_cents
// reflecting how much was drained by usage entries (FIFO via
// ApplyToInvoiceAtomic). Expiry deducts only the REMAINING portion
// (amount - consumed), never the original amount. A fully-consumed
// grant (consumed == amount) yields a 0-cents expiry — skipped
// entirely so the customer's Credits tab doesn't show a meaningless
// "Expired grant X — $0.00" row.
func (s *Service) processExpiry(ctx context.Context, grants []domain.CreditLedgerEntry) (int, []error) {
	var expired int
	var expiryErrs []error
	for _, g := range grants {
		remaining := g.AmountCents - g.ConsumedCents
		if remaining <= 0 {
			// Already fully drained by usage — nothing to expire.
			// Defensive: the SQL filter (consumed_cents < amount_cents)
			// should have excluded this row, but we double-check.
			continue
		}
		// Stamp the expiry entry at the grant's own expires_at — the
		// simulated instant the grant actually expired — instead of
		// the store's clock.Now() fallback (= advance-end frozen_time
		// during catchup, or wall-clock now from the CRON path).
		// Without this, every grant expired in one Advance click
		// stacks at one timestamp on the customer's Credits tab.
		var expiredAt time.Time
		if g.ExpiresAt != nil {
			expiredAt = *g.ExpiresAt
		}
		_, err := s.store.AppendEntry(ctx, g.TenantID, domain.CreditLedgerEntry{
			CustomerID:  g.CustomerID,
			EntryType:   domain.CreditExpiry,
			AmountCents: -remaining,
			Description: fmt.Sprintf("Expired grant %s", g.ID),
			CreatedAt:   expiredAt,
		})
		if err != nil {
			expiryErrs = append(expiryErrs, fmt.Errorf("expire grant %s: %w", g.ID, err))
			continue
		}
		expired++
	}
	return expired, expiryErrs
}

func (s *Service) GetBalance(ctx context.Context, tenantID, customerID string) (domain.CreditBalance, error) {
	return s.store.GetBalance(ctx, tenantID, customerID)
}

func (s *Service) ListBalances(ctx context.Context, tenantID string) ([]domain.CreditBalance, error) {
	return s.store.ListBalances(ctx, tenantID)
}

func (s *Service) ListEntries(ctx context.Context, filter ListFilter) ([]domain.CreditLedgerEntry, error) {
	return s.store.ListEntries(ctx, filter)
}

type AdjustInput struct {
	CustomerID  string `json:"customer_id"`
	AmountCents int64  `json:"amount_cents"` // Positive or negative
	Description string `json:"description"`
}

func (s *Service) Adjust(ctx context.Context, tenantID string, input AdjustInput) (domain.CreditLedgerEntry, error) {
	if input.CustomerID == "" {
		return domain.CreditLedgerEntry{}, errs.Required("customer_id")
	}
	// Bind effective-now from the customer pin so a deduct/credit on a
	// clock-pinned customer stamps the ledger entry's created_at in
	// simulated time — same shape as Grant (line 77). Without this, the
	// AdjustAtomic path falls back to clock.Now(ctx) = wall-clock and the
	// deduction row appears out-of-band on the customer's Credits tab.
	// Audit follow-up to feedback_ctx_attr_audit (2026-05-24): Grant
	// bound, Adjust didn't.
	ctx = s.bindForCustomer(ctx, tenantID, input.CustomerID)
	if input.AmountCents == 0 {
		return domain.CreditLedgerEntry{}, errs.Invalid("amount_cents", "cannot be zero")
	}
	desc := strings.TrimSpace(input.Description)
	if desc == "" {
		return domain.CreditLedgerEntry{}, errs.Required("description")
	}

	return s.store.AdjustAtomic(ctx, tenantID, input.CustomerID, desc, input.AmountCents)
}
