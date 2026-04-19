package invoice

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
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

const invCols = `id, tenant_id, customer_id, subscription_id, invoice_number, status,
	payment_status, currency, subtotal_cents, discount_cents, tax_amount_cents, tax_rate_bp,
	COALESCE(tax_name,''), COALESCE(tax_country,''), COALESCE(tax_id,''),
	total_amount_cents, amount_due_cents, amount_paid_cents, credits_applied_cents,
	billing_period_start, billing_period_end, issued_at, due_at, paid_at, voided_at,
	COALESCE(stripe_payment_intent_id,''), COALESCE(last_payment_error,''),
	payment_overdue, auto_charge_pending, net_payment_term_days, COALESCE(memo,''), COALESCE(footer,''),
	metadata, created_at, updated_at, source_plan_changed_at`

func (s *PostgresStore) Create(ctx context.Context, tenantID string, inv domain.Invoice) (domain.Invoice, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Invoice{}, err
	}
	defer postgres.Rollback(tx)

	id := postgres.NewID("vlx_inv")
	now := time.Now().UTC()
	metaJSON, _ := json.Marshal(inv.Metadata)
	if inv.Metadata == nil {
		metaJSON = []byte("{}")
	}

	err = tx.QueryRowContext(ctx, `
		INSERT INTO invoices (id, tenant_id, customer_id, subscription_id, invoice_number,
			status, payment_status, currency, subtotal_cents, discount_cents, tax_amount_cents, tax_name,
			tax_country, tax_id,
			total_amount_cents, amount_due_cents, amount_paid_cents, credits_applied_cents,
			billing_period_start, billing_period_end, issued_at, due_at,
			net_payment_term_days, memo, footer, metadata, created_at, updated_at,
			source_plan_changed_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$27,$28)
		RETURNING `+invCols,
		id, tenantID, inv.CustomerID, inv.SubscriptionID, inv.InvoiceNumber,
		inv.Status, inv.PaymentStatus, inv.Currency,
		inv.SubtotalCents, inv.DiscountCents, inv.TaxAmountCents, inv.TaxName,
		inv.TaxCountry, inv.TaxID,
		inv.TotalAmountCents, inv.AmountDueCents, inv.AmountPaidCents, inv.CreditsAppliedCents,
		inv.BillingPeriodStart, inv.BillingPeriodEnd,
		postgres.NullableTime(inv.IssuedAt), postgres.NullableTime(inv.DueAt),
		inv.NetPaymentTermDays, postgres.NullableString(inv.Memo),
		postgres.NullableString(inv.Footer), metaJSON, now,
		postgres.NullableTime(inv.SourcePlanChangedAt),
	).Scan(scanInvDest(&inv)...)

	if err != nil {
		if postgres.IsUniqueViolation(err) {
			return domain.Invoice{}, fmt.Errorf("%w: invoice number %q", errs.ErrAlreadyExists, inv.InvoiceNumber)
		}
		return domain.Invoice{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Invoice{}, err
	}
	return inv, nil
}

func (s *PostgresStore) Get(ctx context.Context, tenantID, id string) (domain.Invoice, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Invoice{}, err
	}
	defer postgres.Rollback(tx)

	var inv domain.Invoice
	err = tx.QueryRowContext(ctx, `SELECT `+invCols+` FROM invoices WHERE id = $1`, id).
		Scan(scanInvDest(&inv)...)
	if err == sql.ErrNoRows {
		return domain.Invoice{}, errs.ErrNotFound
	}
	return inv, err
}

// GetByProrationSource returns the invoice previously generated for a
// specific (subscription, plan_changed_at) event, if any. Callers use this
// after CreateWithLineItems returns ErrAlreadyExists to complete an
// idempotent retry — the proration dedup index ensures uniqueness.
func (s *PostgresStore) GetByProrationSource(ctx context.Context, tenantID, subscriptionID string, planChangedAt time.Time) (domain.Invoice, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Invoice{}, err
	}
	defer postgres.Rollback(tx)

	var inv domain.Invoice
	err = tx.QueryRowContext(ctx, `SELECT `+invCols+`
		FROM invoices
		WHERE tenant_id = $1 AND subscription_id = $2 AND source_plan_changed_at = $3`,
		tenantID, subscriptionID, planChangedAt,
	).Scan(scanInvDest(&inv)...)
	if err == sql.ErrNoRows {
		return domain.Invoice{}, errs.ErrNotFound
	}
	return inv, err
}

func (s *PostgresStore) GetByNumber(ctx context.Context, tenantID, number string) (domain.Invoice, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Invoice{}, err
	}
	defer postgres.Rollback(tx)

	var inv domain.Invoice
	err = tx.QueryRowContext(ctx, `SELECT `+invCols+` FROM invoices WHERE invoice_number = $1`, number).
		Scan(scanInvDest(&inv)...)
	if err == sql.ErrNoRows {
		return domain.Invoice{}, errs.ErrNotFound
	}
	return inv, err
}

func (s *PostgresStore) List(ctx context.Context, filter ListFilter) ([]domain.Invoice, int, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, filter.TenantID)
	if err != nil {
		return nil, 0, err
	}
	defer postgres.Rollback(tx)

	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	where, args := buildInvWhere(filter)

	var total int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM invoices`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	query := `SELECT ` + invCols + ` FROM invoices` + where +
		` ORDER BY created_at DESC LIMIT $` + fmt.Sprintf("%d", len(args)+1) +
		` OFFSET $` + fmt.Sprintf("%d", len(args)+2)
	args = append(args, limit, filter.Offset)

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()

	var invoices []domain.Invoice
	for rows.Next() {
		var inv domain.Invoice
		if err := rows.Scan(scanInvDest(&inv)...); err != nil {
			return nil, 0, err
		}
		invoices = append(invoices, inv)
	}
	return invoices, total, rows.Err()
}

func (s *PostgresStore) UpdateStatus(ctx context.Context, tenantID, id string, status domain.InvoiceStatus) (domain.Invoice, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Invoice{}, err
	}
	defer postgres.Rollback(tx)

	now := time.Now().UTC()
	var voidedAt *time.Time
	if status == domain.InvoiceVoided {
		voidedAt = &now
	}

	var inv domain.Invoice
	err = tx.QueryRowContext(ctx, `
		UPDATE invoices SET status = $1, voided_at = $2, updated_at = $3
		WHERE id = $4
		RETURNING `+invCols,
		status, postgres.NullableTime(voidedAt), now, id,
	).Scan(scanInvDest(&inv)...)

	if err == sql.ErrNoRows {
		return domain.Invoice{}, errs.ErrNotFound
	}
	if err != nil {
		return domain.Invoice{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Invoice{}, err
	}
	return inv, nil
}

func (s *PostgresStore) UpdatePayment(ctx context.Context, tenantID, id string, paymentStatus domain.InvoicePaymentStatus, stripePaymentIntentID, lastPaymentError string, paidAt *time.Time) (domain.Invoice, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Invoice{}, err
	}
	defer postgres.Rollback(tx)

	now := time.Now().UTC()
	var inv domain.Invoice
	err = tx.QueryRowContext(ctx, `
		UPDATE invoices SET payment_status = $1, stripe_payment_intent_id = $2,
			last_payment_error = $3, paid_at = $4, updated_at = $5
		WHERE id = $6
		RETURNING `+invCols,
		paymentStatus, postgres.NullableString(stripePaymentIntentID),
		postgres.NullableString(lastPaymentError), postgres.NullableTime(paidAt), now, id,
	).Scan(scanInvDest(&inv)...)

	if err == sql.ErrNoRows {
		return domain.Invoice{}, errs.ErrNotFound
	}
	if err != nil {
		return domain.Invoice{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Invoice{}, err
	}
	return inv, nil
}

func (s *PostgresStore) MarkPaid(ctx context.Context, tenantID, id string, stripePaymentIntentID string, paidAt time.Time) (domain.Invoice, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Invoice{}, err
	}
	defer postgres.Rollback(tx)

	now := time.Now().UTC()
	var inv domain.Invoice
	err = tx.QueryRowContext(ctx, `
		UPDATE invoices SET
			status = 'paid',
			payment_status = 'succeeded',
			stripe_payment_intent_id = $1,
			paid_at = $2,
			amount_paid_cents = amount_due_cents,
			amount_due_cents = 0,
			updated_at = $3
		WHERE id = $4
		RETURNING `+invCols,
		postgres.NullableString(stripePaymentIntentID), paidAt, now, id,
	).Scan(scanInvDest(&inv)...)

	if err == sql.ErrNoRows {
		return domain.Invoice{}, errs.ErrNotFound
	}
	if err != nil {
		return domain.Invoice{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Invoice{}, err
	}
	return inv, nil
}

func (s *PostgresStore) ApplyCreditNote(ctx context.Context, tenantID, id string, amountCents int64) (domain.Invoice, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Invoice{}, err
	}
	defer postgres.Rollback(tx)

	now := time.Now().UTC()
	var inv domain.Invoice
	err = tx.QueryRowContext(ctx, `
		UPDATE invoices SET
			amount_due_cents = GREATEST(amount_due_cents - $1, 0),
			updated_at = $2
		WHERE id = $3
		RETURNING `+invCols,
		amountCents, now, id,
	).Scan(scanInvDest(&inv)...)

	if err == sql.ErrNoRows {
		return domain.Invoice{}, errs.ErrNotFound
	}
	if err != nil {
		return domain.Invoice{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Invoice{}, err
	}
	return inv, nil
}

// ApplyCredits reduces amount_due and tracks the prepaid credits applied during billing.
func (s *PostgresStore) ApplyCredits(ctx context.Context, tenantID, id string, amountCents int64) (domain.Invoice, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Invoice{}, err
	}
	defer postgres.Rollback(tx)

	now := time.Now().UTC()
	var inv domain.Invoice
	err = tx.QueryRowContext(ctx, `
		UPDATE invoices SET
			amount_due_cents = GREATEST(amount_due_cents - $1, 0),
			credits_applied_cents = credits_applied_cents + $1,
			updated_at = $2
		WHERE id = $3
		RETURNING `+invCols,
		amountCents, now, id,
	).Scan(scanInvDest(&inv)...)

	if err == sql.ErrNoRows {
		return domain.Invoice{}, errs.ErrNotFound
	}
	if err != nil {
		return domain.Invoice{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Invoice{}, err
	}
	return inv, nil
}

func (s *PostgresStore) UpdateTotals(ctx context.Context, tenantID, id string, subtotal, total, amountDue int64) (domain.Invoice, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Invoice{}, err
	}
	defer postgres.Rollback(tx)

	now := time.Now().UTC()
	var inv domain.Invoice
	err = tx.QueryRowContext(ctx, `
		UPDATE invoices SET subtotal_cents = $1, total_amount_cents = $2, amount_due_cents = $3, updated_at = $4
		WHERE id = $5
		RETURNING `+invCols,
		subtotal, total, amountDue, now, id,
	).Scan(scanInvDest(&inv)...)

	if err == sql.ErrNoRows {
		return domain.Invoice{}, errs.ErrNotFound
	}
	if err != nil {
		return domain.Invoice{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Invoice{}, err
	}
	return inv, nil
}

func (s *PostgresStore) CreateLineItem(ctx context.Context, tenantID string, item domain.InvoiceLineItem) (domain.InvoiceLineItem, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.InvoiceLineItem{}, err
	}
	defer postgres.Rollback(tx)

	id := postgres.NewID("vlx_ili")
	now := time.Now().UTC()
	metaJSON, _ := json.Marshal(item.Metadata)
	if item.Metadata == nil {
		metaJSON = []byte("{}")
	}

	err = tx.QueryRowContext(ctx, `
		INSERT INTO invoice_line_items (id, invoice_id, tenant_id, line_type, meter_id,
			description, quantity, unit_amount_cents, amount_cents, tax_rate_bp, tax_amount_cents,
			total_amount_cents, currency, pricing_mode, rating_rule_version_id,
			billing_period_start, billing_period_end, metadata, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)
		RETURNING id, invoice_id, tenant_id, line_type, COALESCE(meter_id,''), description,
			quantity, unit_amount_cents, amount_cents, tax_rate_bp, tax_amount_cents,
			total_amount_cents, currency, COALESCE(pricing_mode,''),
			COALESCE(rating_rule_version_id,''), billing_period_start, billing_period_end,
			metadata, created_at
	`, id, item.InvoiceID, tenantID, item.LineType, postgres.NullableString(item.MeterID),
		item.Description, item.Quantity, item.UnitAmountCents, item.AmountCents,
		item.TaxRateBP, item.TaxAmountCents, item.TotalAmountCents, item.Currency,
		postgres.NullableString(item.PricingMode), postgres.NullableString(item.RatingRuleVersionID),
		postgres.NullableTime(item.BillingPeriodStart), postgres.NullableTime(item.BillingPeriodEnd),
		metaJSON, now,
	).Scan(&item.ID, &item.InvoiceID, &item.TenantID, &item.LineType, &item.MeterID,
		&item.Description, &item.Quantity, &item.UnitAmountCents, &item.AmountCents,
		&item.TaxRateBP, &item.TaxAmountCents, &item.TotalAmountCents, &item.Currency,
		&item.PricingMode, &item.RatingRuleVersionID,
		&item.BillingPeriodStart, &item.BillingPeriodEnd, &metaJSON, &item.CreatedAt)

	if err != nil {
		return domain.InvoiceLineItem{}, err
	}
	_ = json.Unmarshal(metaJSON, &item.Metadata)
	if err := tx.Commit(); err != nil {
		return domain.InvoiceLineItem{}, err
	}
	return item, nil
}

func (s *PostgresStore) ListLineItems(ctx context.Context, tenantID, invoiceID string) ([]domain.InvoiceLineItem, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	rows, err := tx.QueryContext(ctx, `
		SELECT id, invoice_id, tenant_id, line_type, COALESCE(meter_id,''), description,
			quantity, unit_amount_cents, amount_cents, tax_rate_bp, tax_amount_cents,
			total_amount_cents, currency, COALESCE(pricing_mode,''),
			COALESCE(rating_rule_version_id,''), billing_period_start, billing_period_end,
			metadata, created_at
		FROM invoice_line_items WHERE invoice_id = $1
		ORDER BY created_at ASC
	`, invoiceID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var items []domain.InvoiceLineItem
	for rows.Next() {
		var item domain.InvoiceLineItem
		var metaJSON []byte
		if err := rows.Scan(&item.ID, &item.InvoiceID, &item.TenantID, &item.LineType,
			&item.MeterID, &item.Description, &item.Quantity, &item.UnitAmountCents,
			&item.AmountCents, &item.TaxRateBP, &item.TaxAmountCents, &item.TotalAmountCents,
			&item.Currency, &item.PricingMode, &item.RatingRuleVersionID,
			&item.BillingPeriodStart, &item.BillingPeriodEnd, &metaJSON, &item.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(metaJSON, &item.Metadata)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *PostgresStore) ListApproachingDue(ctx context.Context, daysBeforeDue int) ([]domain.Invoice, error) {
	// Cross-tenant query for the billing scheduler — bypass RLS.
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	rows, err := tx.QueryContext(ctx, `
		SELECT `+invCols+` FROM invoices
		WHERE status = 'finalized'
		  AND payment_status IN ('pending', 'failed')
		  AND due_at IS NOT NULL
		  AND due_at BETWEEN NOW() AND NOW() + INTERVAL '1 day' * $1
		  AND amount_due_cents > 0
		ORDER BY due_at ASC
		LIMIT 500
	`, daysBeforeDue)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var invoices []domain.Invoice
	for rows.Next() {
		var inv domain.Invoice
		if err := rows.Scan(scanInvDest(&inv)...); err != nil {
			return nil, err
		}
		invoices = append(invoices, inv)
	}
	return invoices, rows.Err()
}

// AddLineItemAtomic inserts a line item and recomputes invoice totals in a
// single transaction, locking the invoice row FOR UPDATE so concurrent
// AddLineItem calls serialize on that row and subtotal reflects every
// committed line item. Fails if the invoice isn't in draft status.
func (s *PostgresStore) AddLineItemAtomic(
	ctx context.Context, tenantID, invoiceID string, item domain.InvoiceLineItem,
) (domain.InvoiceLineItem, domain.Invoice, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.InvoiceLineItem{}, domain.Invoice{}, err
	}
	defer postgres.Rollback(tx)

	var (
		status   domain.InvoiceStatus
		currency string
	)
	err = tx.QueryRowContext(ctx,
		`SELECT status, currency FROM invoices WHERE id = $1 FOR UPDATE`,
		invoiceID,
	).Scan(&status, &currency)
	if err == sql.ErrNoRows {
		return domain.InvoiceLineItem{}, domain.Invoice{}, errs.ErrNotFound
	}
	if err != nil {
		return domain.InvoiceLineItem{}, domain.Invoice{}, err
	}
	if status != domain.InvoiceDraft {
		return domain.InvoiceLineItem{}, domain.Invoice{},
			fmt.Errorf("can only add line items to draft invoices, current status: %s", status)
	}

	item.InvoiceID = invoiceID
	item.TenantID = tenantID
	item.Currency = currency

	itemID := postgres.NewID("vlx_ili")
	now := time.Now().UTC()
	metaJSON, _ := json.Marshal(item.Metadata)
	if item.Metadata == nil {
		metaJSON = []byte("{}")
	}

	err = tx.QueryRowContext(ctx, `
		INSERT INTO invoice_line_items (id, invoice_id, tenant_id, line_type, meter_id,
			description, quantity, unit_amount_cents, amount_cents, tax_rate_bp, tax_amount_cents,
			total_amount_cents, currency, pricing_mode, rating_rule_version_id,
			billing_period_start, billing_period_end, metadata, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)
		RETURNING id, invoice_id, tenant_id, line_type, COALESCE(meter_id,''), description,
			quantity, unit_amount_cents, amount_cents, tax_rate_bp, tax_amount_cents,
			total_amount_cents, currency, COALESCE(pricing_mode,''),
			COALESCE(rating_rule_version_id,''), billing_period_start, billing_period_end,
			metadata, created_at
	`, itemID, invoiceID, tenantID, item.LineType, postgres.NullableString(item.MeterID),
		item.Description, item.Quantity, item.UnitAmountCents, item.AmountCents,
		item.TaxRateBP, item.TaxAmountCents, item.TotalAmountCents, currency,
		postgres.NullableString(item.PricingMode), postgres.NullableString(item.RatingRuleVersionID),
		postgres.NullableTime(item.BillingPeriodStart), postgres.NullableTime(item.BillingPeriodEnd),
		metaJSON, now,
	).Scan(&item.ID, &item.InvoiceID, &item.TenantID, &item.LineType, &item.MeterID,
		&item.Description, &item.Quantity, &item.UnitAmountCents, &item.AmountCents,
		&item.TaxRateBP, &item.TaxAmountCents, &item.TotalAmountCents, &item.Currency,
		&item.PricingMode, &item.RatingRuleVersionID,
		&item.BillingPeriodStart, &item.BillingPeriodEnd, &metaJSON, &item.CreatedAt)
	if err != nil {
		return domain.InvoiceLineItem{}, domain.Invoice{}, err
	}
	_ = json.Unmarshal(metaJSON, &item.Metadata)

	// Recompute subtotal from ALL line items now in the tx (including the one
	// just inserted), then rewrite derived totals. Using a correlated subquery
	// in one UPDATE so the read and write stay in the same snapshot.
	var inv domain.Invoice
	err = tx.QueryRowContext(ctx, `
		UPDATE invoices i SET
			subtotal_cents = sub.subtotal,
			total_amount_cents = sub.subtotal + i.tax_amount_cents - i.discount_cents,
			amount_due_cents = GREATEST(
				sub.subtotal + i.tax_amount_cents - i.discount_cents
					- i.amount_paid_cents - i.credits_applied_cents, 0),
			updated_at = $2
		FROM (
			SELECT COALESCE(SUM(amount_cents), 0)::BIGINT AS subtotal
			FROM invoice_line_items WHERE invoice_id = $1
		) sub
		WHERE i.id = $1
		RETURNING `+invCols,
		invoiceID, now,
	).Scan(scanInvDest(&inv)...)
	if err != nil {
		return domain.InvoiceLineItem{}, domain.Invoice{}, err
	}

	if err := tx.Commit(); err != nil {
		return domain.InvoiceLineItem{}, domain.Invoice{}, err
	}
	return item, inv, nil
}

// CreateWithLineItems creates an invoice and all its line items in a single
// atomic transaction. This prevents orphaned invoices with missing line items.
// The unique index on (tenant_id, subscription_id, billing_period_start, billing_period_end)
// provides idempotency — duplicate calls return an error.
func (s *PostgresStore) CreateWithLineItems(ctx context.Context, tenantID string, inv domain.Invoice, items []domain.InvoiceLineItem) (domain.Invoice, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Invoice{}, err
	}
	defer postgres.Rollback(tx)

	id := postgres.NewID("vlx_inv")
	now := time.Now().UTC()
	metaJSON, _ := json.Marshal(inv.Metadata)
	if inv.Metadata == nil {
		metaJSON = []byte("{}")
	}

	err = tx.QueryRowContext(ctx, `
		INSERT INTO invoices (id, tenant_id, customer_id, subscription_id, invoice_number,
			status, payment_status, currency, subtotal_cents, discount_cents, tax_amount_cents,
			tax_rate_bp, tax_name, tax_country, tax_id,
			total_amount_cents, amount_due_cents, amount_paid_cents, credits_applied_cents,
			billing_period_start, billing_period_end, issued_at, due_at,
			net_payment_term_days, memo, footer, metadata, created_at, updated_at,
			source_plan_changed_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$28,$29)
		RETURNING `+invCols,
		id, tenantID, inv.CustomerID, inv.SubscriptionID, inv.InvoiceNumber,
		inv.Status, inv.PaymentStatus, inv.Currency,
		inv.SubtotalCents, inv.DiscountCents, inv.TaxAmountCents, inv.TaxRateBP,
		inv.TaxName,
		inv.TaxCountry, inv.TaxID,
		inv.TotalAmountCents, inv.AmountDueCents, inv.AmountPaidCents, inv.CreditsAppliedCents,
		inv.BillingPeriodStart, inv.BillingPeriodEnd,
		postgres.NullableTime(inv.IssuedAt), postgres.NullableTime(inv.DueAt),
		inv.NetPaymentTermDays, postgres.NullableString(inv.Memo),
		postgres.NullableString(inv.Footer), metaJSON, now,
		postgres.NullableTime(inv.SourcePlanChangedAt),
	).Scan(scanInvDest(&inv)...)

	if err != nil {
		if postgres.IsUniqueViolation(err) {
			return domain.Invoice{}, fmt.Errorf("%w: invoice already exists for this billing period", errs.ErrAlreadyExists)
		}
		return domain.Invoice{}, err
	}

	// Create all line items within the same transaction
	for i := range items {
		items[i].InvoiceID = inv.ID
		itemID := postgres.NewID("vlx_ili")
		itemMetaJSON, _ := json.Marshal(items[i].Metadata)
		if items[i].Metadata == nil {
			itemMetaJSON = []byte("{}")
		}

		_, err = tx.ExecContext(ctx, `
			INSERT INTO invoice_line_items (id, invoice_id, tenant_id, line_type, meter_id,
				description, quantity, unit_amount_cents, amount_cents, tax_rate_bp,
				tax_amount_cents, total_amount_cents, currency, pricing_mode,
				rating_rule_version_id, billing_period_start, billing_period_end, metadata, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)
		`, itemID, inv.ID, tenantID, items[i].LineType, postgres.NullableString(items[i].MeterID),
			items[i].Description, items[i].Quantity, items[i].UnitAmountCents, items[i].AmountCents,
			items[i].TaxRateBP, items[i].TaxAmountCents, items[i].TotalAmountCents,
			items[i].Currency, postgres.NullableString(items[i].PricingMode),
			postgres.NullableString(items[i].RatingRuleVersionID),
			postgres.NullableTime(items[i].BillingPeriodStart), postgres.NullableTime(items[i].BillingPeriodEnd),
			itemMetaJSON, now,
		)
		if err != nil {
			return domain.Invoice{}, fmt.Errorf("create line item %d: %w", i, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return domain.Invoice{}, err
	}
	return inv, nil
}

// SetAutoChargePending marks an invoice for scheduler-based auto-charge retry.
func (s *PostgresStore) SetAutoChargePending(ctx context.Context, tenantID, id string, pending bool) error {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return err
	}
	defer postgres.Rollback(tx)

	_, err = tx.ExecContext(ctx, `UPDATE invoices SET auto_charge_pending = $1, updated_at = $2 WHERE id = $3`,
		pending, time.Now().UTC(), id)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// ListAutoChargePending returns invoices that need auto-charge retry.
func (s *PostgresStore) ListAutoChargePending(ctx context.Context, limit int) ([]domain.Invoice, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	if limit <= 0 {
		limit = 50
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT `+invCols+` FROM invoices
		WHERE auto_charge_pending = TRUE
		  AND payment_status = 'pending'
		  AND status = 'finalized'
		  AND amount_due_cents > 0
		ORDER BY created_at ASC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var invoices []domain.Invoice
	for rows.Next() {
		var inv domain.Invoice
		if err := rows.Scan(scanInvDest(&inv)...); err != nil {
			return nil, err
		}
		invoices = append(invoices, inv)
	}
	return invoices, rows.Err()
}

// MarkPaidBatch atomically marks an invoice as paid (used by MarkPaid — kept for interface compat).
func (s *PostgresStore) MarkPaidBatch(ctx context.Context, tenantID, id string, stripePaymentIntentID string, paidAt time.Time) (domain.Invoice, error) {
	return s.MarkPaid(ctx, tenantID, id, stripePaymentIntentID, paidAt)
}

func scanInvDest(inv *domain.Invoice) []any {
	var metaJSON []byte
	return []any{
		&inv.ID, &inv.TenantID, &inv.CustomerID, &inv.SubscriptionID, &inv.InvoiceNumber,
		&inv.Status, &inv.PaymentStatus, &inv.Currency,
		&inv.SubtotalCents, &inv.DiscountCents, &inv.TaxAmountCents, &inv.TaxRateBP,
		&inv.TaxName, &inv.TaxCountry, &inv.TaxID,
		&inv.TotalAmountCents, &inv.AmountDueCents, &inv.AmountPaidCents, &inv.CreditsAppliedCents,
		&inv.BillingPeriodStart, &inv.BillingPeriodEnd,
		&inv.IssuedAt, &inv.DueAt, &inv.PaidAt, &inv.VoidedAt,
		&inv.StripePaymentIntentID, &inv.LastPaymentError,
		&inv.PaymentOverdue, &inv.AutoChargePending, &inv.NetPaymentTermDays, &inv.Memo, &inv.Footer,
		&metaJSON, &inv.CreatedAt, &inv.UpdatedAt, &inv.SourcePlanChangedAt,
	}
}

func buildInvWhere(f ListFilter) (string, []any) {
	var clauses []string
	var args []any
	idx := 1

	if f.CustomerID != "" {
		clauses = append(clauses, fmt.Sprintf("customer_id = $%d", idx))
		args = append(args, f.CustomerID)
		idx++
	}
	if f.SubscriptionID != "" {
		clauses = append(clauses, fmt.Sprintf("subscription_id = $%d", idx))
		args = append(args, f.SubscriptionID)
		idx++
	}
	if f.Status != "" {
		clauses = append(clauses, fmt.Sprintf("status = $%d", idx))
		args = append(args, f.Status)
		idx++
	}
	if f.PaymentStatus != "" {
		clauses = append(clauses, fmt.Sprintf("payment_status = $%d", idx))
		args = append(args, f.PaymentStatus)
	}

	if len(clauses) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}
