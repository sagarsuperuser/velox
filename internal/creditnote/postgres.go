package creditnote

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

func (s *PostgresStore) Create(ctx context.Context, tenantID string, cn domain.CreditNote) (domain.CreditNote, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.CreditNote{}, err
	}
	defer postgres.Rollback(tx)

	id := postgres.NewID("vlx_cn")
	now := time.Now().UTC()
	metaJSON, _ := json.Marshal(cn.Metadata)
	if cn.Metadata == nil {
		metaJSON = []byte("{}")
	}

	err = tx.QueryRowContext(ctx, `
		INSERT INTO credit_notes (id, tenant_id, invoice_id, customer_id, credit_note_number,
			status, reason, subtotal_cents, tax_amount_cents, total_cents,
			refund_amount_cents, credit_amount_cents, currency, refund_status,
			metadata, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$16)
		RETURNING id, tenant_id, invoice_id, customer_id, credit_note_number,
			status, reason, subtotal_cents, tax_amount_cents, total_cents,
			refund_amount_cents, credit_amount_cents, currency,
			issued_at, voided_at, refund_status, COALESCE(stripe_refund_id,''),
			metadata, created_at, updated_at
	`, id, tenantID, cn.InvoiceID, cn.CustomerID, cn.CreditNoteNumber,
		cn.Status, cn.Reason, cn.SubtotalCents, cn.TaxAmountCents, cn.TotalCents,
		cn.RefundAmountCents, cn.CreditAmountCents, cn.Currency, cn.RefundStatus,
		metaJSON, now,
	).Scan(&cn.ID, &cn.TenantID, &cn.InvoiceID, &cn.CustomerID, &cn.CreditNoteNumber,
		&cn.Status, &cn.Reason, &cn.SubtotalCents, &cn.TaxAmountCents, &cn.TotalCents,
		&cn.RefundAmountCents, &cn.CreditAmountCents, &cn.Currency,
		&cn.IssuedAt, &cn.VoidedAt, &cn.RefundStatus, &cn.StripeRefundID,
		&metaJSON, &cn.CreatedAt, &cn.UpdatedAt)
	if err != nil {
		return domain.CreditNote{}, err
	}
	_ = json.Unmarshal(metaJSON, &cn.Metadata)
	if err := tx.Commit(); err != nil {
		return domain.CreditNote{}, err
	}
	return cn, nil
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
			refund_amount_cents, credit_amount_cents, currency,
			issued_at, voided_at, refund_status, COALESCE(stripe_refund_id,''),
			metadata, created_at, updated_at
		FROM credit_notes WHERE id = $1
	`, id).Scan(&cn.ID, &cn.TenantID, &cn.InvoiceID, &cn.CustomerID, &cn.CreditNoteNumber,
		&cn.Status, &cn.Reason, &cn.SubtotalCents, &cn.TaxAmountCents, &cn.TotalCents,
		&cn.RefundAmountCents, &cn.CreditAmountCents, &cn.Currency,
		&cn.IssuedAt, &cn.VoidedAt, &cn.RefundStatus, &cn.StripeRefundID,
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

	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	query := `SELECT id, tenant_id, invoice_id, customer_id, credit_note_number,
		status, reason, subtotal_cents, tax_amount_cents, total_cents,
		refund_amount_cents, credit_amount_cents, currency,
		issued_at, voided_at, refund_status, COALESCE(stripe_refund_id,''),
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
	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d OFFSET $%d", idx, idx+1)
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
			&cn.RefundAmountCents, &cn.CreditAmountCents, &cn.Currency,
			&cn.IssuedAt, &cn.VoidedAt, &cn.RefundStatus, &cn.StripeRefundID,
			&metaJSON, &cn.CreatedAt, &cn.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(metaJSON, &cn.Metadata)
		notes = append(notes, cn)
	}
	return notes, rows.Err()
}

func (s *PostgresStore) UpdateStatus(ctx context.Context, tenantID, id string, status domain.CreditNoteStatus) (domain.CreditNote, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.CreditNote{}, err
	}
	defer postgres.Rollback(tx)

	now := time.Now().UTC()
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
		status, stripeRefundID, time.Now().UTC(), id)
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
	now := time.Now().UTC()
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
