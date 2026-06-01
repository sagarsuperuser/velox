package creditnote

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

func (s *PostgresStore) Create(ctx context.Context, tenantID string, cn domain.CreditNote) (domain.CreditNote, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.CreditNote{}, err
	}
	defer postgres.Rollback(tx)

	created, err := s.insertCreditNoteTx(ctx, tx, tenantID, cn)
	if err != nil {
		return domain.CreditNote{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.CreditNote{}, err
	}
	return created, nil
}

// CreateUnderInvoiceLock serializes credit-note creation per invoice. It takes
// a transaction-scoped advisory lock keyed on (tenant, invoice), lists the
// invoice's existing credit notes inside the same transaction, hands them to
// `build` (which runs the over-credit / over-refund cap checks and returns the
// credit note to insert), and inserts it — all atomically. A concurrent Create
// for the same invoice blocks on the lock until this transaction commits, then
// sees this credit note in its own list, so the caps can't be bypassed by a
// time-of-check/time-of-use race. The lock releases automatically on commit.
func (s *PostgresStore) CreateUnderInvoiceLock(ctx context.Context, tenantID, invoiceID string, build func(existing []domain.CreditNote) (domain.CreditNote, error)) (domain.CreditNote, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.CreditNote{}, err
	}
	defer postgres.Rollback(tx)

	if _, err := tx.ExecContext(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, tenantID+":"+invoiceID,
	); err != nil {
		return domain.CreditNote{}, fmt.Errorf("acquire credit-note invoice lock: %w", err)
	}

	existing, err := s.listByInvoiceTx(ctx, tx, invoiceID)
	if err != nil {
		return domain.CreditNote{}, err
	}
	cn, err := build(existing)
	if err != nil {
		return domain.CreditNote{}, err
	}
	created, err := s.insertCreditNoteTx(ctx, tx, tenantID, cn)
	if err != nil {
		return domain.CreditNote{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.CreditNote{}, err
	}
	return created, nil
}

func (s *PostgresStore) insertCreditNoteTx(ctx context.Context, tx *sql.Tx, tenantID string, cn domain.CreditNote) (domain.CreditNote, error) {
	id := postgres.NewID("vlx_cn")
	// Honors ctx-bound effective-now (ADR-030) — credit notes against
	// invoices on clock-pinned subs inherit sim-time. Falls back to
	// wall-clock for unbound ctx (production credit notes).
	now := clock.Now(ctx)
	metaJSON, _ := json.Marshal(cn.Metadata)
	if cn.Metadata == nil {
		metaJSON = []byte("{}")
	}

	err := tx.QueryRowContext(ctx, `
		INSERT INTO credit_notes (id, tenant_id, invoice_id, customer_id, credit_note_number,
			status, reason, subtotal_cents, tax_amount_cents, total_cents,
			refund_amount_cents, credit_amount_cents, out_of_band_amount_cents,
			currency, refund_status, metadata, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$17)
		RETURNING id, tenant_id, invoice_id, customer_id, credit_note_number,
			status, reason, subtotal_cents, tax_amount_cents, total_cents,
			refund_amount_cents, credit_amount_cents, out_of_band_amount_cents,
			currency, issued_at, voided_at, refund_status, COALESCE(stripe_refund_id,''),
			COALESCE(tax_transaction_id,''),
			metadata, created_at, updated_at
	`, id, tenantID, cn.InvoiceID, cn.CustomerID, cn.CreditNoteNumber,
		cn.Status, cn.Reason, cn.SubtotalCents, cn.TaxAmountCents, cn.TotalCents,
		cn.RefundAmountCents, cn.CreditAmountCents, cn.OutOfBandAmountCents,
		cn.Currency, cn.RefundStatus, metaJSON, now,
	).Scan(&cn.ID, &cn.TenantID, &cn.InvoiceID, &cn.CustomerID, &cn.CreditNoteNumber,
		&cn.Status, &cn.Reason, &cn.SubtotalCents, &cn.TaxAmountCents, &cn.TotalCents,
		&cn.RefundAmountCents, &cn.CreditAmountCents, &cn.OutOfBandAmountCents,
		&cn.Currency,
		&cn.IssuedAt, &cn.VoidedAt, &cn.RefundStatus, &cn.StripeRefundID,
		&cn.TaxTransactionID,
		&metaJSON, &cn.CreatedAt, &cn.UpdatedAt)
	if err != nil {
		return domain.CreditNote{}, err
	}
	_ = json.Unmarshal(metaJSON, &cn.Metadata)
	return cn, nil
}

// listByInvoiceTx lists every credit note for an invoice inside the caller's
// transaction (used under the advisory lock in CreateUnderInvoiceLock). Mirrors
// List's column projection.
func (s *PostgresStore) listByInvoiceTx(ctx context.Context, tx *sql.Tx, invoiceID string) ([]domain.CreditNote, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, tenant_id, invoice_id, customer_id, credit_note_number,
			status, reason, subtotal_cents, tax_amount_cents, total_cents,
			refund_amount_cents, credit_amount_cents, out_of_band_amount_cents,
			currency, issued_at, voided_at, refund_status, COALESCE(stripe_refund_id,''),
			COALESCE(tax_transaction_id,''),
			metadata, created_at, updated_at
		FROM credit_notes WHERE invoice_id = $1`, invoiceID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var notes []domain.CreditNote
	for rows.Next() {
		var cn domain.CreditNote
		var metaJSON []byte
		if err := rows.Scan(&cn.ID, &cn.TenantID, &cn.InvoiceID, &cn.CustomerID, &cn.CreditNoteNumber,
			&cn.Status, &cn.Reason, &cn.SubtotalCents, &cn.TaxAmountCents, &cn.TotalCents,
			&cn.RefundAmountCents, &cn.CreditAmountCents, &cn.OutOfBandAmountCents,
			&cn.Currency, &cn.IssuedAt, &cn.VoidedAt, &cn.RefundStatus, &cn.StripeRefundID,
			&cn.TaxTransactionID,
			&metaJSON, &cn.CreatedAt, &cn.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(metaJSON, &cn.Metadata)
		notes = append(notes, cn)
	}
	return notes, rows.Err()
}

func (s *PostgresStore) Get(ctx context.Context, tenantID, id string) (domain.CreditNote, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.CreditNote{}, err
	}
	defer postgres.Rollback(tx)

	var cn domain.CreditNote
	var metaJSON []byte
	err = tx.QueryRowContext(ctx, `
		SELECT id, tenant_id, invoice_id, customer_id, credit_note_number,
			status, reason, subtotal_cents, tax_amount_cents, total_cents,
			refund_amount_cents, credit_amount_cents, out_of_band_amount_cents,
			currency, issued_at, voided_at, refund_status, COALESCE(stripe_refund_id,''),
			COALESCE(tax_transaction_id,''),
			metadata, created_at, updated_at
		FROM credit_notes WHERE id = $1
	`, id).Scan(&cn.ID, &cn.TenantID, &cn.InvoiceID, &cn.CustomerID, &cn.CreditNoteNumber,
		&cn.Status, &cn.Reason, &cn.SubtotalCents, &cn.TaxAmountCents, &cn.TotalCents,
		&cn.RefundAmountCents, &cn.CreditAmountCents, &cn.OutOfBandAmountCents,
		&cn.Currency, &cn.IssuedAt, &cn.VoidedAt, &cn.RefundStatus, &cn.StripeRefundID,
		&cn.TaxTransactionID,
		&metaJSON, &cn.CreatedAt, &cn.UpdatedAt)
	if err == sql.ErrNoRows {
		return domain.CreditNote{}, errs.ErrNotFound
	}
	if err != nil {
		return domain.CreditNote{}, err
	}
	_ = json.Unmarshal(metaJSON, &cn.Metadata)
	return cn, nil
}

func (s *PostgresStore) List(ctx context.Context, filter ListFilter) ([]domain.CreditNote, error) {
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

	query := `SELECT id, tenant_id, invoice_id, customer_id, credit_note_number,
		status, reason, subtotal_cents, tax_amount_cents, total_cents,
		refund_amount_cents, credit_amount_cents, out_of_band_amount_cents,
		currency, issued_at, voided_at, refund_status, COALESCE(stripe_refund_id,''),
		COALESCE(tax_transaction_id,''),
		metadata, created_at, updated_at
		FROM credit_notes`
	args := []any{}
	idx := 1
	clauses := []string{}

	if filter.InvoiceID != "" {
		clauses = append(clauses, fmt.Sprintf("invoice_id = $%d", idx))
		args = append(args, filter.InvoiceID)
		idx++
	}
	if filter.Status != "" {
		clauses = append(clauses, fmt.Sprintf("status = $%d", idx))
		args = append(args, filter.Status)
		idx++
	}
	if len(clauses) > 0 {
		query += " WHERE "
		for i, c := range clauses {
			if i > 0 {
				query += " AND "
			}
			query += c
		}
	}
	query += fmt.Sprintf(" ORDER BY %s LIMIT $%d OFFSET $%d", creditNoteOrderBy(filter.Sort, filter.SortDir), idx, idx+1)
	args = append(args, limit, filter.Offset)

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var notes []domain.CreditNote
	for rows.Next() {
		var cn domain.CreditNote
		var metaJSON []byte
		if err := rows.Scan(&cn.ID, &cn.TenantID, &cn.InvoiceID, &cn.CustomerID, &cn.CreditNoteNumber,
			&cn.Status, &cn.Reason, &cn.SubtotalCents, &cn.TaxAmountCents, &cn.TotalCents,
			&cn.RefundAmountCents, &cn.CreditAmountCents, &cn.OutOfBandAmountCents,
			&cn.Currency, &cn.IssuedAt, &cn.VoidedAt, &cn.RefundStatus, &cn.StripeRefundID,
			&cn.TaxTransactionID,
			&metaJSON, &cn.CreatedAt, &cn.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(metaJSON, &cn.Metadata)
		notes = append(notes, cn)
	}
	return notes, rows.Err()
}

// TransitionStatus atomically flips a credit note from `from` to `to` only if
// it is currently in `from`, returning whether this call won the transition.
// The conditional UPDATE ... WHERE status=$from is the compare-and-swap that
// serializes concurrent/retried Issue() calls: exactly one caller gets won=true
// and proceeds to the (non-idempotent) amount_due reduction; the losers get
// won=false and must not re-apply.
func (s *PostgresStore) TransitionStatus(ctx context.Context, tenantID, id string, from, to domain.CreditNoteStatus) (bool, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return false, err
	}
	defer postgres.Rollback(tx)

	now := clock.Now(ctx)
	var issuedAt, voidedAt *time.Time
	if to == domain.CreditNoteIssued {
		issuedAt = &now
	}
	if to == domain.CreditNoteVoided {
		voidedAt = &now
	}

	res, err := tx.ExecContext(ctx, `
		UPDATE credit_notes SET status=$1, issued_at=COALESCE($2, issued_at),
			voided_at=COALESCE($3, voided_at), updated_at=$4
		WHERE id=$5 AND status=$6`,
		to, postgres.NullableTime(issuedAt), postgres.NullableTime(voidedAt), now, id, from)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return n == 1, nil
}

func (s *PostgresStore) UpdateStatus(ctx context.Context, tenantID, id string, status domain.CreditNoteStatus) (domain.CreditNote, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.CreditNote{}, err
	}
	defer postgres.Rollback(tx)

	now := clock.Now(ctx)
	var issuedAt, voidedAt *time.Time
	if status == domain.CreditNoteIssued {
		issuedAt = &now
	}
	if status == domain.CreditNoteVoided {
		voidedAt = &now
	}

	_, err = tx.ExecContext(ctx, `
		UPDATE credit_notes SET status=$1, issued_at=COALESCE($2, issued_at),
			voided_at=COALESCE($3, voided_at), updated_at=$4
		WHERE id=$5`,
		status, postgres.NullableTime(issuedAt), postgres.NullableTime(voidedAt), now, id)
	if err != nil {
		return domain.CreditNote{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.CreditNote{}, err
	}
	return s.Get(ctx, tenantID, id)
}

// SetTaxTransaction persists the upstream reversal transaction id (Stripe:
// tx_xxx) returned by the tax provider at Issue time. Idempotency guard:
// the credit note service checks for a non-empty value before calling
// Provider.Reverse so a retried Issue does not create duplicate reversals.
func (s *PostgresStore) SetTaxTransaction(ctx context.Context, tenantID, id string, taxTransactionID string) error {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return err
	}
	defer postgres.Rollback(tx)

	_, err = tx.ExecContext(ctx, `
		UPDATE credit_notes SET tax_transaction_id = $1, updated_at = $2 WHERE id = $3`,
		taxTransactionID, clock.Now(ctx), id)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *PostgresStore) UpdateRefundStatus(ctx context.Context, tenantID, id string, status domain.RefundStatus, stripeRefundID string) error {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return err
	}
	defer postgres.Rollback(tx)

	_, err = tx.ExecContext(ctx, `
		UPDATE credit_notes SET refund_status=$1, stripe_refund_id=COALESCE(NULLIF($2,''), stripe_refund_id),
			updated_at=$3
		WHERE id=$4`,
		status, stripeRefundID, clock.Now(ctx), id)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateAllocation persists the three-channel allocation. Issue() calls
// this when a CN created against a then-unpaid invoice is issued after
// the invoice became paid — the allocation frozen at Create was all
// zeros, so Issue re-derives it (default: full credit to balance) and
// persists the result here so the dashboard and any retry observe the
// same split Issue acted on.
func (s *PostgresStore) UpdateAllocation(ctx context.Context, tenantID, id string, refundCents, creditCents, outOfBandCents int64) error {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return err
	}
	defer postgres.Rollback(tx)

	_, err = tx.ExecContext(ctx, `
		UPDATE credit_notes SET refund_amount_cents=$1, credit_amount_cents=$2,
			out_of_band_amount_cents=$3, updated_at=$4
		WHERE id=$5`,
		refundCents, creditCents, outOfBandCents, clock.Now(ctx), id)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *PostgresStore) CreateLineItem(ctx context.Context, tenantID string, item domain.CreditNoteLineItem) (domain.CreditNoteLineItem, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.CreditNoteLineItem{}, err
	}
	defer postgres.Rollback(tx)

	id := postgres.NewID("vlx_cnli")
	now := clock.Now(ctx)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO credit_note_line_items (id, credit_note_id, tenant_id,
			invoice_line_item_id, description, quantity, unit_amount_cents, amount_cents, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
	`, id, item.CreditNoteID, tenantID, postgres.NullableString(item.InvoiceLineItemID),
		item.Description, item.Quantity, item.UnitAmountCents, item.AmountCents, now)
	if err != nil {
		return domain.CreditNoteLineItem{}, err
	}
	item.ID = id
	item.TenantID = tenantID
	item.CreatedAt = now
	if err := tx.Commit(); err != nil {
		return domain.CreditNoteLineItem{}, err
	}
	return item, nil
}

func (s *PostgresStore) ListLineItems(ctx context.Context, tenantID, creditNoteID string) ([]domain.CreditNoteLineItem, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	rows, err := tx.QueryContext(ctx, `
		SELECT id, credit_note_id, tenant_id, COALESCE(invoice_line_item_id,''),
			description, quantity, unit_amount_cents, amount_cents, created_at
		FROM credit_note_line_items WHERE credit_note_id = $1
		ORDER BY created_at ASC
	`, creditNoteID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var items []domain.CreditNoteLineItem
	for rows.Next() {
		var item domain.CreditNoteLineItem
		if err := rows.Scan(&item.ID, &item.CreditNoteID, &item.TenantID, &item.InvoiceLineItemID,
			&item.Description, &item.Quantity, &item.UnitAmountCents, &item.AmountCents, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// creditNoteOrderBy validates sort + dir against a closed allow-list
// and adds a deterministic id tie-break matching the primary
// direction. See invoiceOrderBy for the design rationale.
func creditNoteOrderBy(sort, dir string) string {
	col := creditNoteSortColumn(sort)
	d := "DESC"
	if dir == "asc" {
		d = "ASC"
	}
	return col + " " + d + ", id " + d
}

func creditNoteSortColumn(key string) string {
	switch key {
	case "credit_note_number":
		return "credit_note_number"
	case "total_cents", "amount":
		return "total_cents"
	case "status":
		return "status"
	case "issued_at":
		return "issued_at"
	default:
		return "created_at"
	}
}
