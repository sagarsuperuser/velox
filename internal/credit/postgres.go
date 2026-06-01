package credit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/clock"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

type PostgresStore struct {
	db *postgres.DB
}

func NewPostgresStore(db *postgres.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

func (s *PostgresStore) AppendEntry(ctx context.Context, tenantID string, entry domain.CreditLedgerEntry) (domain.CreditLedgerEntry, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.CreditLedgerEntry{}, err
	}
	defer postgres.Rollback(tx)
	out, err := s.appendEntryInTx(ctx, tx, tenantID, entry)
	if err != nil {
		return domain.CreditLedgerEntry{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.CreditLedgerEntry{}, err
	}
	return out, nil
}

// AppendEntryTx is the in-transaction variant used by the subscription
// handler's atomic AddItem+proration flow: the caller has already
// opened a tx wrapping the sub-item insert + invoice/credit insert,
// and this entry needs to share that tx so a failure here rolls back
// the item add too. ADR-030 atomic-proration follow-through.
func (s *PostgresStore) AppendEntryTx(ctx context.Context, tx *sql.Tx, tenantID string, entry domain.CreditLedgerEntry) (domain.CreditLedgerEntry, error) {
	return s.appendEntryInTx(ctx, tx, tenantID, entry)
}

// appendEntryInTx is the shared body. AppendEntry opens+commits its
// own tx; AppendEntryTx delegates to the caller.
func (s *PostgresStore) appendEntryInTx(ctx context.Context, tx *sql.Tx, tenantID string, entry domain.CreditLedgerEntry) (domain.CreditLedgerEntry, error) {
	// Serialize concurrent writes for this customer so two grants can't compute
	// the same balance_after off a stale snapshot. A `SELECT ... FOR UPDATE`
	// over customer_credit_ledger only locks EXISTING rows — for a customer
	// with an empty ledger (the very first grant) it matches zero rows and
	// acquires no lock, so the first concurrent appends raced. A per-customer
	// transaction advisory lock always serializes, regardless of ledger state;
	// it releases automatically on commit/rollback.
	//
	// tenant_id is folded into the lock key as defense-in-depth (RLS already
	// scopes the tx to this tenant).
	if _, err := tx.ExecContext(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		tenantID+":"+entry.CustomerID,
	); err != nil {
		return domain.CreditLedgerEntry{}, fmt.Errorf("acquire credit ledger lock: %w", err)
	}
	var currentBalance int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(amount_cents), 0) FROM customer_credit_ledger WHERE tenant_id = $1 AND customer_id = $2`,
		tenantID, entry.CustomerID,
	).Scan(&currentBalance); err != nil {
		return domain.CreditLedgerEntry{}, err
	}

	entry.BalanceAfter = currentBalance + entry.AmountCents
	entry.ID = postgres.NewID("vlx_ccl")

	metaJSON, _ := json.Marshal(entry.Metadata)
	if entry.Metadata == nil {
		metaJSON = []byte("{}")
	}

	// Honor caller-supplied CreatedAt so ledger entries land at the
	// simulated instant the fact occurred — grant at cycle close,
	// expiry at grant.expires_at, usage at the invoice's issue moment.
	// Without this, every entry stamps clock.Now() (= advance-end
	// frozen_time during catchup) and the customer's Credits tab
	// shows a stack of entries all at one timestamp instead of the
	// per-fact chronology. Falls back to clock.Now() when zero so
	// wall-clock callers stay correct.
	createdAt := entry.CreatedAt
	if createdAt.IsZero() {
		createdAt = clock.Now(ctx)
	}
	err := tx.QueryRowContext(ctx, `
		INSERT INTO customer_credit_ledger (id, tenant_id, customer_id, entry_type,
			amount_cents, balance_after, description, invoice_id, expires_at, metadata, created_at,
			source_subscription_id, source_subscription_item_id, source_plan_changed_at, source_change_type,
			source_credit_note_id, source_invoice_reversal_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		RETURNING id, tenant_id, customer_id, entry_type, amount_cents, balance_after,
			description, COALESCE(invoice_id,''), expires_at, metadata, created_at,
			COALESCE(source_subscription_id,''), COALESCE(source_subscription_item_id,''),
			source_plan_changed_at, COALESCE(source_change_type,''),
			COALESCE(source_credit_note_id,'')
	`, entry.ID, tenantID, entry.CustomerID, entry.EntryType,
		entry.AmountCents, entry.BalanceAfter, entry.Description,
		postgres.NullableString(entry.InvoiceID), postgres.NullableTime(entry.ExpiresAt),
		metaJSON, createdAt,
		postgres.NullableString(entry.SourceSubscriptionID),
		postgres.NullableString(entry.SourceSubscriptionItemID),
		postgres.NullableTime(entry.SourcePlanChangedAt),
		postgres.NullableString(string(entry.SourceChangeType)),
		postgres.NullableString(entry.SourceCreditNoteID),
		postgres.NullableString(entry.SourceInvoiceReversalID),
	).Scan(&entry.ID, &entry.TenantID, &entry.CustomerID, &entry.EntryType,
		&entry.AmountCents, &entry.BalanceAfter, &entry.Description,
		&entry.InvoiceID, &entry.ExpiresAt, &metaJSON, &entry.CreatedAt,
		&entry.SourceSubscriptionID, &entry.SourceSubscriptionItemID, &entry.SourcePlanChangedAt,
		(*string)(&entry.SourceChangeType),
		&entry.SourceCreditNoteID)

	if err != nil {
		if postgres.IsUniqueViolation(err) {
			// Route by constraint name so callers can distinguish a
			// proration-dedup replay (idempotent: re-fetch and return
			// the prior entry) from a credit-note-dedup replay (same
			// shape, different semantic — caller's responsibility to
			// handle). Pre-2026-05-28 every unique violation collapsed
			// into a generic ErrAlreadyExists and the caller assumed it
			// was proration — silent misroute when the credit-note
			// dedup index fired instead. ADR-030 cross-flow audit
			// 2026-05-28 / feedback_no_silent_fallbacks memory.
			switch postgres.UniqueViolationConstraint(err) {
			case "idx_credit_ledger_proration_dedup":
				return domain.CreditLedgerEntry{}, errs.AlreadyExists("proration_source",
					"credit ledger entry already exists for this item change").WithCode("credit_proration_source_taken")
			case "idx_credit_ledger_credit_note_dedup":
				return domain.CreditLedgerEntry{}, errs.AlreadyExists("credit_note_source",
					"credit ledger entry already exists for this credit note").WithCode("credit_note_source_taken")
			case "idx_credit_ledger_reversal_dedup":
				return domain.CreditLedgerEntry{}, errs.AlreadyExists("invoice_reversal_source",
					"credit ledger reversal already exists for this invoice").WithCode("credit_reversal_source_taken")
			}
			return domain.CreditLedgerEntry{}, errs.AlreadyExists("",
				fmt.Sprintf("unique constraint %q violated on credit ledger insert",
					postgres.UniqueViolationConstraint(err))).WithCode("credit_unique_violation")
		}
		if postgres.IsForeignKeyViolation(err) {
			return domain.CreditLedgerEntry{}, fmt.Errorf("customer %q not found", entry.CustomerID)
		}
		return domain.CreditLedgerEntry{}, err
	}
	_ = json.Unmarshal(metaJSON, &entry.Metadata)
	return entry, nil
}

// AdjustAtomic inserts a manual adjustment entry while holding a row lock on
// the customer's ledger, so the balance check and the insert observe the same
// snapshot. Without the lock, two concurrent deductions can each read the
// full balance, each pass "balance + amount >= 0", and both commit — driving
// the ledger negative.
//
// Per-block consumed_cents attribution (Orb credit-block model). A negative
// adjustment (clawback) FIFO-drains across active positive blocks (grants +
// prior positive adjustments + invoice reversals), bumping each block's
// consumed_cents by the drained amount. Without this attribution, balance
// drops below sum(grant.remaining) and subsequent ApplyToInvoiceAtomic or
// processExpiry calls drive the ledger negative — the customer's $30 balance
// + $80 grant remaining drift documented 2026-05-24. Positive adjustments
// don't drain; they're themselves drainable by future operations because
// ApplyToInvoiceAtomic now selects any positive entry, not only grants.
func (s *PostgresStore) AdjustAtomic(
	ctx context.Context, tenantID, customerID, description string, amountCents int64,
) (domain.CreditLedgerEntry, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.CreditLedgerEntry{}, err
	}
	defer postgres.Rollback(tx)

	// Lock customer's ledger rows (defense-in-depth: tenant_id predicate in
	// addition to RLS — see AppendEntry).
	if _, err := tx.ExecContext(ctx,
		`SELECT 1 FROM customer_credit_ledger WHERE tenant_id = $1 AND customer_id = $2 FOR UPDATE`,
		tenantID, customerID,
	); err != nil {
		return domain.CreditLedgerEntry{}, fmt.Errorf("lock credit ledger: %w", err)
	}

	var currentBalance int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(amount_cents), 0) FROM customer_credit_ledger WHERE tenant_id = $1 AND customer_id = $2`,
		tenantID, customerID,
	).Scan(&currentBalance); err != nil {
		return domain.CreditLedgerEntry{}, fmt.Errorf("read credit balance: %w", err)
	}

	if amountCents < 0 && currentBalance+amountCents < 0 {
		return domain.CreditLedgerEntry{}, fmt.Errorf("insufficient balance: available %.2f, deduction %.2f",
			float64(currentBalance)/100, float64(-amountCents)/100)
	}

	now := clock.Now(ctx)

	// Negative adjustment: FIFO-drain positive blocks so each block's
	// consumed_cents stays consistent with the ledger sum. Balance
	// check above guarantees enough drainable; the return value is
	// the actual drained amount and equals -amountCents.
	if amountCents < 0 {
		if _, err := s.drainPositiveBlocks(ctx, tx, tenantID, customerID, -amountCents, now); err != nil {
			return domain.CreditLedgerEntry{}, fmt.Errorf("attribute clawback: %w", err)
		}
	}

	entry := domain.CreditLedgerEntry{
		ID:           postgres.NewID("vlx_ccl"),
		TenantID:     tenantID,
		CustomerID:   customerID,
		EntryType:    domain.CreditAdjustment,
		AmountCents:  amountCents,
		BalanceAfter: currentBalance + amountCents,
		Description:  description,
		CreatedAt:    now,
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO customer_credit_ledger (id, tenant_id, customer_id, entry_type,
			amount_cents, balance_after, description, metadata, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
	`, entry.ID, entry.TenantID, entry.CustomerID, entry.EntryType,
		entry.AmountCents, entry.BalanceAfter, entry.Description, []byte("{}"), entry.CreatedAt,
	); err != nil {
		if postgres.IsForeignKeyViolation(err) {
			return domain.CreditLedgerEntry{}, fmt.Errorf("customer %q not found", customerID)
		}
		return domain.CreditLedgerEntry{}, fmt.Errorf("insert adjustment: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return domain.CreditLedgerEntry{}, err
	}
	return entry, nil
}

// drainPositiveBlocks FIFO-drains up to `wantDrain` cents across active
// positive blocks (grants + positive adjustments + invoice reversals),
// bumping each block's consumed_cents. Returns the actual drained
// amount — which equals min(wantDrain, sum(block.remaining)).
//
// Order: soonest-expiring first (NULL last) so usage minimizes wasted
// expiring credits, earliest-created as the tie-breaker. Skips blocks
// past their expires_at — those will be retired by the expiry path;
// draining them here would mask the retirement.
//
// Caller MUST hold a FOR UPDATE lock on the customer's ledger rows.
// In a clean ledger drainable == balance always holds (every negative
// ledger entry attributes via this path), so callers can pass the
// raw invoice or clawback amount and trust the return value for the
// actual mutation that follows.
func (s *PostgresStore) drainPositiveBlocks(
	ctx context.Context, tx *sql.Tx, tenantID, customerID string, wantDrain int64, now time.Time,
) (int64, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, amount_cents, consumed_cents
		FROM customer_credit_ledger
		WHERE tenant_id = $1
		  AND customer_id = $2
		  AND amount_cents > 0
		  AND consumed_cents < amount_cents
		  AND (expires_at IS NULL OR expires_at > $3)
		ORDER BY expires_at NULLS LAST, created_at, id
	`, tenantID, customerID, now)
	if err != nil {
		return 0, fmt.Errorf("scan positive blocks: %w", err)
	}
	type block struct {
		id        string
		remaining int64
	}
	var blocks []block
	for rows.Next() {
		var id string
		var amount, consumed int64
		if err := rows.Scan(&id, &amount, &consumed); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("scan block: %w", err)
		}
		blocks = append(blocks, block{id: id, remaining: amount - consumed})
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("close blocks cursor: %w", err)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate blocks: %w", err)
	}

	remaining := wantDrain
	for _, b := range blocks {
		if remaining <= 0 {
			break
		}
		take := min(b.remaining, remaining)
		if take <= 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE customer_credit_ledger
			SET consumed_cents = consumed_cents + $1
			WHERE id = $2 AND tenant_id = $3
		`, take, b.id, tenantID); err != nil {
			return 0, fmt.Errorf("update block %s consumed_cents: %w", b.id, err)
		}
		remaining -= take
	}
	return wantDrain - remaining, nil
}

// ApplyToInvoiceAtomic debits the customer's credit balance and reduces the
// invoice's amount_due_cents in a SINGLE transaction. Either both writes
// succeed or both are rolled back. This closes the dual-write hole where a
// credit ledger entry could be created but the invoice's denormalized
// amount_due fail to update, leaving the customer double-billed.
//
// Returns the amount deducted (0 if balance is zero or invoice is free).
// Writes across two tables (customer_credit_ledger + invoices) inside one
// tenant-scoped tx — intentional: the invoice's credit fields are a cache of
// the ledger's source of truth, and they must stay in lockstep.
func (s *PostgresStore) ApplyToInvoiceAtomic(ctx context.Context, tenantID, customerID, invoiceID, invoiceDesc string, invoiceAmountCents int64, at time.Time) (int64, error) {
	if invoiceAmountCents <= 0 {
		return 0, nil
	}

	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return 0, err
	}
	defer postgres.Rollback(tx)

	// Lock the customer's ledger rows to serialize concurrent applications —
	// without this, two simultaneous billing runs on the same customer could
	// each see the full balance and over-deduct.
	//
	// tenant_id included in predicate as defense-in-depth (see AppendEntry).
	if _, err := tx.ExecContext(ctx,
		`SELECT 1 FROM customer_credit_ledger WHERE tenant_id = $1 AND customer_id = $2 FOR UPDATE`,
		tenantID, customerID,
	); err != nil {
		return 0, fmt.Errorf("lock credit ledger: %w", err)
	}

	var currentBalance int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(amount_cents), 0) FROM customer_credit_ledger WHERE tenant_id = $1 AND customer_id = $2`,
		tenantID, customerID,
	).Scan(&currentBalance); err != nil {
		return 0, fmt.Errorf("read credit balance: %w", err)
	}

	if currentBalance <= 0 {
		return 0, nil // No credits to apply — no need to write anything.
	}

	now := at
	if now.IsZero() {
		now = clock.Now(ctx)
	}

	// FIFO-drain active positive blocks (Orb's credit-block model).
	// drainPositiveBlocks returns the actual drained amount (≤
	// requested) — partial-application (invoice > balance) naturally
	// drains only what's available; the leftover stays on the
	// invoice for the PaymentIntent to cover. Negative balance was
	// short-circuited above.
	deduct, err := s.drainPositiveBlocks(ctx, tx, tenantID, customerID, invoiceAmountCents, now)
	if err != nil {
		return 0, fmt.Errorf("drain credits for invoice: %w", err)
	}
	if deduct <= 0 {
		return 0, nil
	}
	if deduct <= 0 {
		// All "balance" was in expired grants — nothing actually drainable.
		return 0, nil
	}

	entryID := postgres.NewID("vlx_ccl")
	balanceAfter := currentBalance - deduct

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO customer_credit_ledger (id, tenant_id, customer_id, entry_type,
			amount_cents, balance_after, description, invoice_id, metadata, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
	`, entryID, tenantID, customerID, domain.CreditUsage,
		-deduct, balanceAfter, invoiceDesc, invoiceID, []byte("{}"), now,
	); err != nil {
		if postgres.IsForeignKeyViolation(err) {
			return 0, fmt.Errorf("customer %q not found", customerID)
		}
		return 0, fmt.Errorf("insert credit usage: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE invoices
		SET amount_due_cents = GREATEST(amount_due_cents - $1, 0),
		    credits_applied_cents = credits_applied_cents + $1,
		    updated_at = $2
		WHERE id = $3 AND tenant_id = $4
	`, deduct, now, invoiceID, tenantID); err != nil {
		return 0, fmt.Errorf("update invoice amount_due: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit credit application: %w", err)
	}
	return deduct, nil
}

// GetByProrationSource returns the credit ledger entry previously written
// for a specific (subscription, item, change_type, change_at) event, if any.
// Callers use this after AppendEntry returns ErrAlreadyExists to complete an
// idempotent retry — the proration dedup partial index ensures uniqueness.
// change_type disambiguates plan-vs-qty-vs-remove events on the same item at
// the same wall-clock instant; the item id keeps cross-item changes distinct.
func (s *PostgresStore) GetByProrationSource(ctx context.Context, tenantID, subscriptionID, subscriptionItemID string, changeType domain.ItemChangeType, changeAt time.Time) (domain.CreditLedgerEntry, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.CreditLedgerEntry{}, err
	}
	defer postgres.Rollback(tx)

	var e domain.CreditLedgerEntry
	var metaJSON []byte
	err = tx.QueryRowContext(ctx, `
		SELECT id, tenant_id, customer_id, entry_type, amount_cents, balance_after,
			description, COALESCE(invoice_id,''), expires_at, metadata, created_at,
			COALESCE(source_subscription_id,''), COALESCE(source_subscription_item_id,''),
			source_plan_changed_at, COALESCE(source_change_type,'')
		FROM customer_credit_ledger
		WHERE tenant_id = $1 AND source_subscription_id = $2 AND source_subscription_item_id = $3 AND source_change_type = $4 AND source_plan_changed_at = $5
	`, tenantID, subscriptionID, subscriptionItemID, string(changeType), changeAt,
	).Scan(&e.ID, &e.TenantID, &e.CustomerID, &e.EntryType,
		&e.AmountCents, &e.BalanceAfter, &e.Description, &e.InvoiceID,
		&e.ExpiresAt, &metaJSON, &e.CreatedAt,
		&e.SourceSubscriptionID, &e.SourceSubscriptionItemID, &e.SourcePlanChangedAt,
		(*string)(&e.SourceChangeType))
	if err == sql.ErrNoRows {
		return domain.CreditLedgerEntry{}, errs.ErrNotFound
	}
	if err != nil {
		return domain.CreditLedgerEntry{}, err
	}
	_ = json.Unmarshal(metaJSON, &e.Metadata)
	return e, nil
}

// GetByCreditNoteSource fetches the grant row tied to a specific
// credit-note Issue(). Used by credit.Service.GrantForCreditNote to
// recover from ErrAlreadyExists on retry — the partial unique index
// idx_credit_ledger_credit_note_dedup (migration 0093) enforces one
// grant per (tenant, CN), so a retry that hits this lookup confirms
// the grant already exists and returns it for the caller to use.
func (s *PostgresStore) GetByCreditNoteSource(ctx context.Context, tenantID, creditNoteID string) (domain.CreditLedgerEntry, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.CreditLedgerEntry{}, err
	}
	defer postgres.Rollback(tx)

	var e domain.CreditLedgerEntry
	var metaJSON []byte
	err = tx.QueryRowContext(ctx, `
		SELECT id, tenant_id, customer_id, entry_type, amount_cents, balance_after,
			description, COALESCE(invoice_id,''), expires_at, metadata, created_at,
			COALESCE(source_subscription_id,''), COALESCE(source_subscription_item_id,''),
			source_plan_changed_at, COALESCE(source_change_type,''),
			COALESCE(source_credit_note_id,'')
		FROM customer_credit_ledger
		WHERE tenant_id = $1 AND source_credit_note_id = $2
	`, tenantID, creditNoteID,
	).Scan(&e.ID, &e.TenantID, &e.CustomerID, &e.EntryType,
		&e.AmountCents, &e.BalanceAfter, &e.Description, &e.InvoiceID,
		&e.ExpiresAt, &metaJSON, &e.CreatedAt,
		&e.SourceSubscriptionID, &e.SourceSubscriptionItemID, &e.SourcePlanChangedAt,
		(*string)(&e.SourceChangeType),
		&e.SourceCreditNoteID)
	if err == sql.ErrNoRows {
		return domain.CreditLedgerEntry{}, errs.ErrNotFound
	}
	if err != nil {
		return domain.CreditLedgerEntry{}, err
	}
	_ = json.Unmarshal(metaJSON, &e.Metadata)
	return e, nil
}

func (s *PostgresStore) GetBalance(ctx context.Context, tenantID, customerID string) (domain.CreditBalance, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.CreditBalance{}, err
	}
	defer postgres.Rollback(tx)

	var b domain.CreditBalance
	b.CustomerID = customerID

	err = tx.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(amount_cents), 0),
			COALESCE(SUM(CASE WHEN amount_cents > 0 THEN amount_cents ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN amount_cents < 0 THEN ABS(amount_cents) ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN entry_type = 'expiry' THEN ABS(amount_cents) ELSE 0 END), 0)
		FROM customer_credit_ledger WHERE tenant_id = $1 AND customer_id = $2
	`, tenantID, customerID).Scan(&b.BalanceCents, &b.TotalGranted, &b.TotalUsed, &b.TotalExpired)

	return b, err
}

func (s *PostgresStore) ListBalances(ctx context.Context, tenantID string) ([]domain.CreditBalance, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	rows, err := tx.QueryContext(ctx, `
		SELECT
			customer_id,
			COALESCE(SUM(amount_cents), 0),
			COALESCE(SUM(CASE WHEN amount_cents > 0 THEN amount_cents ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN amount_cents < 0 THEN ABS(amount_cents) ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN entry_type = 'expiry' THEN ABS(amount_cents) ELSE 0 END), 0)
		FROM customer_credit_ledger
		WHERE tenant_id = $1
		GROUP BY customer_id
		ORDER BY SUM(amount_cents) DESC
	`, tenantID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var balances []domain.CreditBalance
	for rows.Next() {
		var b domain.CreditBalance
		if err := rows.Scan(&b.CustomerID, &b.BalanceCents, &b.TotalGranted, &b.TotalUsed, &b.TotalExpired); err != nil {
			return nil, err
		}
		balances = append(balances, b)
	}
	return balances, rows.Err()
}

// ListExpiredGrants — CRON path. ADR-029 Phase 4: clock-pinned
// customers' grants are excluded; the catchup orchestrator drives
// per-clock expiry via ListExpiredGrantsForClock against the clock's
// frozen_time. Without this filter the wall-clock cron would expire
// a clock-pinned customer's grants based on wall-clock time, not the
// simulated time the customer's other consequences run on.
func (s *PostgresStore) ListExpiredGrants(ctx context.Context) ([]domain.CreditLedgerEntry, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	// expires_at + consumed_cents fetched so processExpiry can:
	//   (a) stamp the expiry row's created_at at the grant's actual
	//       expires_at (the simulated/wall-clock moment it expired)
	//       instead of falling back to clock.Now() which would land
	//       at the CRON tick time — making the customer's Credits
	//       timeline lie about when each grant actually expired.
	//   (b) skip grants that are already fully consumed (amount ==
	//       consumed_cents) — expiring them would double-deduct the
	//       balance since the usage entries already drained them.
	rows, err := tx.QueryContext(ctx, `
		SELECT g.id, g.tenant_id, g.customer_id, g.amount_cents, g.consumed_cents, g.expires_at
		FROM customer_credit_ledger g
		WHERE g.entry_type = 'grant'
		  AND g.expires_at IS NOT NULL
		  AND g.expires_at < NOW()
		  AND g.livemode = $1
		  AND g.consumed_cents < g.amount_cents
		  AND NOT EXISTS (
		    SELECT 1 FROM customers c
		    WHERE c.id = g.customer_id
		      AND c.test_clock_id IS NOT NULL
		  )
		  AND NOT EXISTS (
		    SELECT 1 FROM customer_credit_ledger e2
		    WHERE e2.customer_id = g.customer_id
		      AND e2.entry_type = 'expiry'
		      AND e2.description LIKE 'Expired grant %' || g.id || '%'
		  )
	`, postgres.Livemode(ctx))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var entries []domain.CreditLedgerEntry
	for rows.Next() {
		var e domain.CreditLedgerEntry
		if err := rows.Scan(&e.ID, &e.TenantID, &e.CustomerID, &e.AmountCents, &e.ConsumedCents, &e.ExpiresAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// ListExpiredGrantsForClock is the catchup-path counterpart. ADR-029
// Phase 4 — returns grants belonging to customers pinned to the given
// clock whose expires_at is past the clock's frozen_time (passed
// explicitly so the comparison happens in simulated time, not wall-
// clock now).
func (s *PostgresStore) ListExpiredGrantsForClock(ctx context.Context, tenantID, clockID string, frozenTime time.Time) ([]domain.CreditLedgerEntry, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	// expires_at + consumed_cents fetched (see ListExpiredGrants
	// rationale): processExpiry stamps the expiry row at the grant's
	// expires_at (correct timeline) and skips fully-consumed grants
	// (no double-deduct).
	rows, err := tx.QueryContext(ctx, `
		SELECT g.id, g.tenant_id, g.customer_id, g.amount_cents, g.consumed_cents, g.expires_at
		FROM customer_credit_ledger g
		JOIN customers c ON c.id = g.customer_id
		WHERE g.entry_type = 'grant'
		  AND g.expires_at IS NOT NULL
		  AND g.expires_at < $3
		  AND g.tenant_id = $1
		  AND c.test_clock_id = $2
		  AND g.consumed_cents < g.amount_cents
		  AND NOT EXISTS (
		    SELECT 1 FROM customer_credit_ledger e2
		    WHERE e2.customer_id = g.customer_id
		      AND e2.entry_type = 'expiry'
		      AND e2.description LIKE 'Expired grant %' || g.id || '%'
		  )
	`, tenantID, clockID, frozenTime)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var entries []domain.CreditLedgerEntry
	for rows.Next() {
		var e domain.CreditLedgerEntry
		if err := rows.Scan(&e.ID, &e.TenantID, &e.CustomerID, &e.AmountCents, &e.ConsumedCents, &e.ExpiresAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func (s *PostgresStore) ListEntries(ctx context.Context, filter ListFilter) ([]domain.CreditLedgerEntry, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, filter.TenantID)
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	// Default 50, clamp to 100 — no-silent-fallbacks principle.
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	} else if limit > 100 {
		limit = 100
	}

	// Chronological running balance via SUM(amount_cents) OVER window.
	// The stored balance_after column reflects the SUM at INSERTION
	// time — which diverges from "balance as of this row's date" when
	// rows are inserted out of chronological order (catchup-stamped
	// expiry rows land with a past created_at AFTER newer rows
	// already exist). Without this override, the UI's Balance column
	// reads as gibberish for any customer who's had a catchup-driven
	// expiry processed. Industry parity (Stripe / Lago / Orb): credit
	// timelines show the chronological running balance, not insertion-
	// order snapshots.
	//
	// Window is computed over the FULL customer ledger (no entry_type
	// or invoice_id filter inside the window) so the running balance
	// stays correct even when the operator filters the view to
	// "usage only" or one invoice — they still see "balance after
	// this row's chronological moment" rather than "balance after
	// this row's row-order moment."
	//
	// CTE pattern: compute running balance over all rows, THEN apply
	// filter + sort + pagination on the outer query.
	query := `WITH ledger AS (
		SELECT id, tenant_id, customer_id, entry_type, amount_cents,
			SUM(amount_cents) OVER (
				PARTITION BY tenant_id, customer_id
				ORDER BY created_at, id
				ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW
			) AS running_balance,
			description, invoice_id, expires_at, metadata, created_at
		FROM customer_credit_ledger
		WHERE tenant_id = $1 AND customer_id = $2
	)
	SELECT id, tenant_id, customer_id, entry_type, amount_cents,
		running_balance AS balance_after,
		description, COALESCE(invoice_id,''), expires_at, metadata, created_at
	FROM ledger WHERE 1=1`
	args := []any{filter.TenantID, filter.CustomerID}
	idx := 3

	if filter.InvoiceID != "" {
		query += fmt.Sprintf(" AND invoice_id = $%d", idx)
		args = append(args, filter.InvoiceID)
		idx++
	}
	if filter.EntryType != "" {
		query += fmt.Sprintf(" AND entry_type = $%d", idx)
		args = append(args, filter.EntryType)
		idx++
	}

	query += fmt.Sprintf(" ORDER BY %s LIMIT $%d OFFSET $%d", creditEntryOrderBy(filter.Sort, filter.SortDir), idx, idx+1)
	args = append(args, limit, filter.Offset)

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var entries []domain.CreditLedgerEntry
	for rows.Next() {
		var e domain.CreditLedgerEntry
		var metaJSON []byte
		if err := rows.Scan(&e.ID, &e.TenantID, &e.CustomerID, &e.EntryType,
			&e.AmountCents, &e.BalanceAfter, &e.Description, &e.InvoiceID,
			&e.ExpiresAt, &metaJSON, &e.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(metaJSON, &e.Metadata)
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// creditEntryOrderBy validates sort + dir against a closed allow-list
// and adds a deterministic id tie-break matching the primary
// direction. See invoiceOrderBy for the design rationale.
func creditEntryOrderBy(sort, dir string) string {
	col := creditEntrySortColumn(sort)
	d := "DESC"
	if dir == "asc" {
		d = "ASC"
	}
	return col + " " + d + ", id " + d
}

func creditEntrySortColumn(key string) string {
	switch key {
	case "amount_cents", "amount":
		return "amount_cents"
	case "entry_type":
		return "entry_type"
	case "expires_at":
		return "expires_at"
	default:
		return "created_at"
	}
}
