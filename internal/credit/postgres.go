package credit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
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

	// Lock existing rows for this customer to serialize concurrent writes.
	// This prevents two concurrent grants from computing the same balance_after.
	// We lock first, then aggregate — FOR UPDATE can't be used directly with aggregates in all cases.
	//
	// tenant_id is included in every predicate as defense-in-depth: RLS (TxTenant)
	// already restricts rows to this tenant, but if RLS were ever misconfigured or
	// a future refactor opened a tx without tenant scope, these filters prevent
	// cross-tenant balance leakage.
	_, _ = tx.ExecContext(ctx,
		`SELECT 1 FROM customer_credit_ledger WHERE tenant_id = $1 AND customer_id = $2 FOR UPDATE`,
		tenantID, entry.CustomerID,
	)
	var currentBalance int64
	err = tx.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(amount_cents), 0) FROM customer_credit_ledger WHERE tenant_id = $1 AND customer_id = $2`,
		tenantID, entry.CustomerID,
	).Scan(&currentBalance)
	if err != nil {
		return domain.CreditLedgerEntry{}, err
	}

	entry.BalanceAfter = currentBalance + entry.AmountCents
	entry.ID = postgres.NewID("vlx_ccl")

	metaJSON, _ := json.Marshal(entry.Metadata)
	if entry.Metadata == nil {
		metaJSON = []byte("{}")
	}

	err = tx.QueryRowContext(ctx, `
		INSERT INTO customer_credit_ledger (id, tenant_id, customer_id, entry_type,
			amount_cents, balance_after, description, invoice_id, expires_at, metadata, created_at,
			source_subscription_id, source_subscription_item_id, source_plan_changed_at, source_change_type)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
		RETURNING id, tenant_id, customer_id, entry_type, amount_cents, balance_after,
			description, COALESCE(invoice_id,''), expires_at, metadata, created_at,
			COALESCE(source_subscription_id,''), COALESCE(source_subscription_item_id,''),
			source_plan_changed_at, COALESCE(source_change_type,'')
	`, entry.ID, tenantID, entry.CustomerID, entry.EntryType,
		entry.AmountCents, entry.BalanceAfter, entry.Description,
		postgres.NullableString(entry.InvoiceID), postgres.NullableTime(entry.ExpiresAt),
		metaJSON, time.Now().UTC(),
		postgres.NullableString(entry.SourceSubscriptionID),
		postgres.NullableString(entry.SourceSubscriptionItemID),
		postgres.NullableTime(entry.SourcePlanChangedAt),
		postgres.NullableString(string(entry.SourceChangeType)),
	).Scan(&entry.ID, &entry.TenantID, &entry.CustomerID, &entry.EntryType,
		&entry.AmountCents, &entry.BalanceAfter, &entry.Description,
		&entry.InvoiceID, &entry.ExpiresAt, &metaJSON, &entry.CreatedAt,
		&entry.SourceSubscriptionID, &entry.SourceSubscriptionItemID, &entry.SourcePlanChangedAt,
		(*string)(&entry.SourceChangeType))

	if err != nil {
		if postgres.IsUniqueViolation(err) {
			// Proration dedup index fired — the caller is retrying a grant
			// that already succeeded for this (subscription, plan_changed_at).
			// Return ErrAlreadyExists so the handler can re-fetch and return
			// the original entry rather than double-crediting.
			return domain.CreditLedgerEntry{}, errs.ErrAlreadyExists
		}
		if postgres.IsForeignKeyViolation(err) {
			return domain.CreditLedgerEntry{}, fmt.Errorf("customer %q not found", entry.CustomerID)
		}
		return domain.CreditLedgerEntry{}, err
	}
	_ = json.Unmarshal(metaJSON, &entry.Metadata)
	if err := tx.Commit(); err != nil {
		return domain.CreditLedgerEntry{}, err
	}
	return entry, nil
}

// AdjustAtomic inserts a manual adjustment entry while holding a row lock on
// the customer's ledger, so the balance check and the insert observe the same
// snapshot. Without the lock, two concurrent deductions can each read the
// full balance, each pass "balance + amount >= 0", and both commit — driving
// the ledger negative.
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

	entry := domain.CreditLedgerEntry{
		ID:           postgres.NewID("vlx_ccl"),
		TenantID:     tenantID,
		CustomerID:   customerID,
		EntryType:    domain.CreditAdjustment,
		AmountCents:  amountCents,
		BalanceAfter: currentBalance + amountCents,
		Description:  description,
		CreatedAt:    time.Now().UTC(),
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
func (s *PostgresStore) ApplyToInvoiceAtomic(ctx context.Context, tenantID, customerID, invoiceID, invoiceDesc string, invoiceAmountCents int64) (int64, error) {
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

	deduct := min(currentBalance, invoiceAmountCents)

	entryID := postgres.NewID("vlx_ccl")
	balanceAfter := currentBalance - deduct
	now := time.Now().UTC()

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

func (s *PostgresStore) ListExpiredGrants(ctx context.Context) ([]domain.CreditLedgerEntry, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	// TxBypass crosses tenants for the credit-expiry sweep; livemode filter
	// ensures test-mode expiries don't append under live ctx (see #13).
	rows, err := tx.QueryContext(ctx, `
		SELECT id, tenant_id, customer_id, amount_cents
		FROM customer_credit_ledger
		WHERE entry_type = 'grant'
		  AND expires_at IS NOT NULL
		  AND expires_at < NOW()
		  AND livemode = $1
		  AND NOT EXISTS (
		    SELECT 1 FROM customer_credit_ledger e2
		    WHERE e2.customer_id = customer_credit_ledger.customer_id
		      AND e2.entry_type = 'expiry'
		      AND e2.description LIKE 'Expired grant %' || customer_credit_ledger.id || '%'
		  )
	`, postgres.Livemode(ctx))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var entries []domain.CreditLedgerEntry
	for rows.Next() {
		var e domain.CreditLedgerEntry
		if err := rows.Scan(&e.ID, &e.TenantID, &e.CustomerID, &e.AmountCents); err != nil {
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

	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	query := `SELECT id, tenant_id, customer_id, entry_type, amount_cents, balance_after,
		description, COALESCE(invoice_id,''), expires_at, metadata, created_at
		FROM customer_credit_ledger WHERE tenant_id = $1 AND customer_id = $2`
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

	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d OFFSET $%d", idx, idx+1)
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
