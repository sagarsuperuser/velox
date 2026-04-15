package credit

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
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
	_, _ = tx.ExecContext(ctx,
		`SELECT 1 FROM customer_credit_ledger WHERE customer_id = $1 FOR UPDATE`,
		entry.CustomerID,
	)
	var currentBalance int64
	err = tx.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(amount_cents), 0) FROM customer_credit_ledger WHERE customer_id = $1`,
		entry.CustomerID,
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
			amount_cents, balance_after, description, invoice_id, expires_at, metadata, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		RETURNING id, tenant_id, customer_id, entry_type, amount_cents, balance_after,
			description, COALESCE(invoice_id,''), expires_at, metadata, created_at
	`, entry.ID, tenantID, entry.CustomerID, entry.EntryType,
		entry.AmountCents, entry.BalanceAfter, entry.Description,
		postgres.NullableString(entry.InvoiceID), postgres.NullableTime(entry.ExpiresAt),
		metaJSON, time.Now().UTC(),
	).Scan(&entry.ID, &entry.TenantID, &entry.CustomerID, &entry.EntryType,
		&entry.AmountCents, &entry.BalanceAfter, &entry.Description,
		&entry.InvoiceID, &entry.ExpiresAt, &metaJSON, &entry.CreatedAt)

	if err != nil {
		if postgres.IsForeignKeyViolation(err) {
			return domain.CreditLedgerEntry{}, fmt.Errorf("customer %q not found", entry.CustomerID)
		}
		return domain.CreditLedgerEntry{}, err
	}
	json.Unmarshal(metaJSON, &entry.Metadata)
	if err := tx.Commit(); err != nil {
		return domain.CreditLedgerEntry{}, err
	}
	return entry, nil
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
		FROM customer_credit_ledger WHERE customer_id = $1
	`, customerID).Scan(&b.BalanceCents, &b.TotalGranted, &b.TotalUsed, &b.TotalExpired)

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
		GROUP BY customer_id
		ORDER BY SUM(amount_cents) DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

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

	rows, err := tx.QueryContext(ctx, `
		SELECT id, tenant_id, customer_id, amount_cents
		FROM customer_credit_ledger
		WHERE entry_type = 'grant'
		  AND expires_at IS NOT NULL
		  AND expires_at < NOW()
		  AND NOT EXISTS (
		    SELECT 1 FROM customer_credit_ledger e2
		    WHERE e2.customer_id = customer_credit_ledger.customer_id
		      AND e2.entry_type = 'expiry'
		      AND e2.description LIKE 'Expired grant %' || customer_credit_ledger.id || '%'
		  )
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

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
		FROM customer_credit_ledger WHERE customer_id = $1`
	args := []any{filter.CustomerID}
	idx := 2

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
	defer rows.Close()

	var entries []domain.CreditLedgerEntry
	for rows.Next() {
		var e domain.CreditLedgerEntry
		var metaJSON []byte
		if err := rows.Scan(&e.ID, &e.TenantID, &e.CustomerID, &e.EntryType,
			&e.AmountCents, &e.BalanceAfter, &e.Description, &e.InvoiceID,
			&e.ExpiresAt, &metaJSON, &e.CreatedAt); err != nil {
			return nil, err
		}
		json.Unmarshal(metaJSON, &e.Metadata)
		entries = append(entries, e)
	}
	return entries, rows.Err()
}
