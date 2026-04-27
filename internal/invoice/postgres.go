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

const invCols = `id, tenant_id, customer_id, COALESCE(subscription_id,''), invoice_number, status,
	payment_status, currency, subtotal_cents, discount_cents, tax_amount_cents, tax_rate_bp,
	COALESCE(tax_name,''), COALESCE(tax_country,''), COALESCE(tax_id,''),
	total_amount_cents, amount_due_cents, amount_paid_cents, credits_applied_cents,
	billing_period_start, billing_period_end, issued_at, due_at, paid_at, voided_at,
	COALESCE(stripe_payment_intent_id,''), COALESCE(last_payment_error,''),
	payment_overdue, auto_charge_pending, net_payment_term_days, COALESCE(memo,''), COALESCE(footer,''),
	metadata, created_at, updated_at, source_plan_changed_at, COALESCE(source_subscription_item_id,''),
	COALESCE(source_change_type,''),
	tax_provider, tax_calculation_id, COALESCE(tax_transaction_id,''),
	tax_reverse_charge, tax_exempt_reason,
	tax_status, tax_deferred_at, tax_retry_count, tax_pending_reason,
	COALESCE(public_token,''), COALESCE(billing_reason,''), COALESCE(stripe_invoice_id,'')`

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

	taxStatus := inv.TaxStatus
	if taxStatus == "" {
		taxStatus = domain.InvoiceTaxOK
	}
	err = tx.QueryRowContext(ctx, `
		INSERT INTO invoices (id, tenant_id, customer_id, subscription_id, invoice_number,
			status, payment_status, currency, subtotal_cents, discount_cents, tax_amount_cents, tax_name,
			tax_country, tax_id,
			total_amount_cents, amount_due_cents, amount_paid_cents, credits_applied_cents,
			billing_period_start, billing_period_end, issued_at, due_at,
			net_payment_term_days, memo, footer, metadata, created_at, updated_at,
			source_plan_changed_at, source_subscription_item_id, source_change_type,
			tax_provider, tax_calculation_id, tax_reverse_charge, tax_exempt_reason,
			tax_status, tax_deferred_at, tax_retry_count, tax_pending_reason, billing_reason,
			stripe_invoice_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$27,$28,$29,$30,$31,$32,$33,$34,$35,$36,$37,$38,$39,$40)
		RETURNING `+invCols,
		id, tenantID, inv.CustomerID, postgres.NullableString(inv.SubscriptionID), inv.InvoiceNumber,
		inv.Status, inv.PaymentStatus, inv.Currency,
		inv.SubtotalCents, inv.DiscountCents, inv.TaxAmountCents, inv.TaxName,
		inv.TaxCountry, inv.TaxID,
		inv.TotalAmountCents, inv.AmountDueCents, inv.AmountPaidCents, inv.CreditsAppliedCents,
		inv.BillingPeriodStart, inv.BillingPeriodEnd,
		postgres.NullableTime(inv.IssuedAt), postgres.NullableTime(inv.DueAt),
		inv.NetPaymentTermDays, postgres.NullableString(inv.Memo),
		postgres.NullableString(inv.Footer), metaJSON, now,
		postgres.NullableTime(inv.SourcePlanChangedAt),
		postgres.NullableString(inv.SourceSubscriptionItemID),
		postgres.NullableString(string(inv.SourceChangeType)),
		inv.TaxProvider, inv.TaxCalculationID, inv.TaxReverseCharge, inv.TaxExemptReason,
		string(taxStatus), postgres.NullableTime(inv.TaxDeferredAt), inv.TaxRetryCount, inv.TaxPendingReason,
		postgres.NullableString(string(inv.BillingReason)),
		postgres.NullableString(inv.StripeInvoiceID),
	).Scan(scanInvDest(&inv)...)

	if err != nil {
		if postgres.IsUniqueViolation(err) {
			return domain.Invoice{}, errs.AlreadyExists("invoice_number", fmt.Sprintf("invoice number %q already exists", inv.InvoiceNumber))
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
// specific (subscription, item, change_type, change_at) event, if any.
// Callers use this after CreateWithLineItems returns ErrAlreadyExists to
// complete an idempotent retry — the proration dedup index ensures
// uniqueness. change_type disambiguates plan-vs-quantity-vs-add-vs-remove
// mutations that coincidentally share a wall-clock timestamp; the item id
// keeps cross-item changes in the same transaction distinct.
func (s *PostgresStore) GetByProrationSource(ctx context.Context, tenantID, subscriptionID, subscriptionItemID string, changeType domain.ItemChangeType, changeAt time.Time) (domain.Invoice, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Invoice{}, err
	}
	defer postgres.Rollback(tx)

	var inv domain.Invoice
	err = tx.QueryRowContext(ctx, `SELECT `+invCols+`
		FROM invoices
		WHERE tenant_id = $1 AND subscription_id = $2 AND source_subscription_item_id = $3 AND source_change_type = $4 AND source_plan_changed_at = $5`,
		tenantID, subscriptionID, subscriptionItemID, string(changeType), changeAt,
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
			billing_period_start, billing_period_end, metadata, created_at,
			tax_jurisdiction, tax_code)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)
		RETURNING id, invoice_id, tenant_id, line_type, COALESCE(meter_id,''), description,
			quantity, unit_amount_cents, amount_cents, tax_rate_bp, tax_amount_cents,
			total_amount_cents, currency, COALESCE(pricing_mode,''),
			COALESCE(rating_rule_version_id,''), billing_period_start, billing_period_end,
			metadata, created_at, tax_jurisdiction, tax_code
	`, id, item.InvoiceID, tenantID, item.LineType, postgres.NullableString(item.MeterID),
		item.Description, item.Quantity, item.UnitAmountCents, item.AmountCents,
		item.TaxRateBP, item.TaxAmountCents, item.TotalAmountCents, item.Currency,
		postgres.NullableString(item.PricingMode), postgres.NullableString(item.RatingRuleVersionID),
		postgres.NullableTime(item.BillingPeriodStart), postgres.NullableTime(item.BillingPeriodEnd),
		metaJSON, now, item.TaxJurisdiction, item.TaxCode,
	).Scan(&item.ID, &item.InvoiceID, &item.TenantID, &item.LineType, &item.MeterID,
		&item.Description, &item.Quantity, &item.UnitAmountCents, &item.AmountCents,
		&item.TaxRateBP, &item.TaxAmountCents, &item.TotalAmountCents, &item.Currency,
		&item.PricingMode, &item.RatingRuleVersionID,
		&item.BillingPeriodStart, &item.BillingPeriodEnd, &metaJSON, &item.CreatedAt,
		&item.TaxJurisdiction, &item.TaxCode)

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
			metadata, created_at, tax_jurisdiction, tax_code
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
			&item.BillingPeriodStart, &item.BillingPeriodEnd, &metaJSON, &item.CreatedAt,
			&item.TaxJurisdiction, &item.TaxCode); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(metaJSON, &item.Metadata)
		items = append(items, item)
	}
	return items, rows.Err()
}

// HasSucceededInvoice reports whether the customer has any invoice with
// payment_status = 'succeeded'. EXISTS + LIMIT 1 so the query hits the index
// on (tenant_id, customer_id, payment_status) without scanning history.
func (s *PostgresStore) HasSucceededInvoice(ctx context.Context, tenantID, customerID string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return false, err
	}
	defer postgres.Rollback(tx)

	var found bool
	err = tx.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM invoices
			WHERE customer_id = $1 AND payment_status = 'succeeded'
			LIMIT 1
		)
	`, customerID).Scan(&found)
	if err != nil {
		return false, err
	}
	return found, nil
}

func (s *PostgresStore) ListApproachingDue(ctx context.Context, daysBeforeDue int) ([]domain.Invoice, error) {
	// Cross-tenant query for the billing scheduler — bypass RLS.
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	// TxBypass crosses tenants for reminder sweep; livemode filter ensures
	// reminders only fire for invoices in the ctx's mode (see #13).
	rows, err := tx.QueryContext(ctx, `
		SELECT `+invCols+` FROM invoices
		WHERE status = 'finalized'
		  AND payment_status IN ('pending', 'failed')
		  AND due_at IS NOT NULL
		  AND due_at BETWEEN NOW() AND NOW() + INTERVAL '1 day' * $1
		  AND amount_due_cents > 0
		  AND livemode = $2
		ORDER BY due_at ASC
		LIMIT 500
	`, daysBeforeDue, postgres.Livemode(ctx))
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
			billing_period_start, billing_period_end, metadata, created_at,
			tax_jurisdiction, tax_code)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)
		RETURNING id, invoice_id, tenant_id, line_type, COALESCE(meter_id,''), description,
			quantity, unit_amount_cents, amount_cents, tax_rate_bp, tax_amount_cents,
			total_amount_cents, currency, COALESCE(pricing_mode,''),
			COALESCE(rating_rule_version_id,''), billing_period_start, billing_period_end,
			metadata, created_at, tax_jurisdiction, tax_code
	`, itemID, invoiceID, tenantID, item.LineType, postgres.NullableString(item.MeterID),
		item.Description, item.Quantity, item.UnitAmountCents, item.AmountCents,
		item.TaxRateBP, item.TaxAmountCents, item.TotalAmountCents, currency,
		postgres.NullableString(item.PricingMode), postgres.NullableString(item.RatingRuleVersionID),
		postgres.NullableTime(item.BillingPeriodStart), postgres.NullableTime(item.BillingPeriodEnd),
		metaJSON, now, item.TaxJurisdiction, item.TaxCode,
	).Scan(&item.ID, &item.InvoiceID, &item.TenantID, &item.LineType, &item.MeterID,
		&item.Description, &item.Quantity, &item.UnitAmountCents, &item.AmountCents,
		&item.TaxRateBP, &item.TaxAmountCents, &item.TotalAmountCents, &item.Currency,
		&item.PricingMode, &item.RatingRuleVersionID,
		&item.BillingPeriodStart, &item.BillingPeriodEnd, &metaJSON, &item.CreatedAt,
		&item.TaxJurisdiction, &item.TaxCode)
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

// ApplyDiscountAtomic stamps a coupon discount (and the recomputed tax
// snapshot) onto a draft invoice in a single transaction. The gate
// re-check lives inside the tx — the caller's outer validate-then-write
// pattern would race a concurrent finalize or a parallel apply-coupon
// attempt; taking FOR UPDATE and re-asserting status=draft,
// discount_cents=0, tax_transaction_id IS NULL closes the TOCTOU window.
//
// Per-line tax fields (tax_rate_bp, tax_amount_cents, total_amount_cents,
// amount_cents) are rewritten from the caller's supplied lineItems because
// the post-discount tax base may shift the per-line splits (inclusive-mode
// carving in particular). Lines keyed by id; ids that don't belong to this
// invoice are silently ignored so a caller can't corrupt a sibling.
func (s *PostgresStore) ApplyDiscountAtomic(
	ctx context.Context, tenantID, invoiceID string,
	update domain.InvoiceDiscountUpdate, lineItems []domain.InvoiceLineItem,
) (domain.Invoice, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Invoice{}, err
	}
	defer postgres.Rollback(tx)

	var (
		status           domain.InvoiceStatus
		existingDiscount int64
		taxTransactionID string
	)
	err = tx.QueryRowContext(ctx, `
		SELECT status, discount_cents, COALESCE(tax_transaction_id,'')
		FROM invoices WHERE id = $1 FOR UPDATE
	`, invoiceID).Scan(&status, &existingDiscount, &taxTransactionID)
	if err == sql.ErrNoRows {
		return domain.Invoice{}, errs.ErrNotFound
	}
	if err != nil {
		return domain.Invoice{}, err
	}
	if status != domain.InvoiceDraft {
		return domain.Invoice{}, errs.InvalidState(fmt.Sprintf(
			"invoice must be draft to apply a coupon (current: %s)", status))
	}
	if existingDiscount > 0 {
		return domain.Invoice{}, errs.InvalidState("invoice already has a discount applied")
	}
	if taxTransactionID != "" {
		return domain.Invoice{}, errs.InvalidState("invoice tax has already been committed upstream")
	}

	now := time.Now().UTC()

	for _, li := range lineItems {
		if li.ID == "" {
			continue
		}
		_, err = tx.ExecContext(ctx, `
			UPDATE invoice_line_items
			SET amount_cents = $3,
			    tax_rate_bp = $4,
			    tax_amount_cents = $5,
			    total_amount_cents = $6
			WHERE invoice_id = $1 AND id = $2
		`, invoiceID, li.ID, li.AmountCents, li.TaxRateBP, li.TaxAmountCents, li.TotalAmountCents)
		if err != nil {
			return domain.Invoice{}, fmt.Errorf("update line item tax stamp: %w", err)
		}
	}

	total := update.SubtotalCents - update.DiscountCents + update.TaxAmountCents

	var inv domain.Invoice
	err = tx.QueryRowContext(ctx, `
		UPDATE invoices SET
			subtotal_cents = $2,
			discount_cents = $3,
			tax_amount_cents = $4,
			tax_rate_bp = $5,
			tax_name = $6,
			tax_country = $7,
			tax_id = $8,
			tax_provider = $9,
			tax_calculation_id = $10,
			tax_reverse_charge = $11,
			tax_exempt_reason = $12,
			tax_status = $13,
			tax_deferred_at = $14,
			tax_pending_reason = $15,
			total_amount_cents = $16,
			amount_due_cents = GREATEST($16 - amount_paid_cents - credits_applied_cents, 0),
			updated_at = $17
		WHERE id = $1
		RETURNING `+invCols,
		invoiceID,
		update.SubtotalCents, update.DiscountCents, update.TaxAmountCents, update.TaxRateBP,
		update.TaxName, update.TaxCountry, update.TaxID,
		postgres.NullableString(update.TaxProvider),
		postgres.NullableString(update.TaxCalculationID),
		update.TaxReverseCharge, update.TaxExemptReason,
		update.TaxStatus, postgres.NullableTime(update.TaxDeferredAt), update.TaxPendingReason,
		total, now,
	).Scan(scanInvDest(&inv)...)
	if err != nil {
		return domain.Invoice{}, err
	}

	if err := tx.Commit(); err != nil {
		return domain.Invoice{}, err
	}
	return inv, nil
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

	taxStatus := inv.TaxStatus
	if taxStatus == "" {
		taxStatus = domain.InvoiceTaxOK
	}
	err = tx.QueryRowContext(ctx, `
		INSERT INTO invoices (id, tenant_id, customer_id, subscription_id, invoice_number,
			status, payment_status, currency, subtotal_cents, discount_cents, tax_amount_cents,
			tax_rate_bp, tax_name, tax_country, tax_id,
			total_amount_cents, amount_due_cents, amount_paid_cents, credits_applied_cents,
			billing_period_start, billing_period_end, issued_at, due_at,
			net_payment_term_days, memo, footer, metadata, created_at, updated_at,
			source_plan_changed_at, source_subscription_item_id, source_change_type,
			tax_provider, tax_calculation_id, tax_reverse_charge, tax_exempt_reason,
			tax_status, tax_deferred_at, tax_retry_count, tax_pending_reason, billing_reason,
			stripe_invoice_id, paid_at, voided_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$28,$29,$30,$31,$32,$33,$34,$35,$36,$37,$38,$39,$40,$41,$42,$43)
		RETURNING `+invCols,
		id, tenantID, inv.CustomerID, postgres.NullableString(inv.SubscriptionID), inv.InvoiceNumber,
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
		postgres.NullableString(inv.SourceSubscriptionItemID),
		postgres.NullableString(string(inv.SourceChangeType)),
		inv.TaxProvider, inv.TaxCalculationID, inv.TaxReverseCharge, inv.TaxExemptReason,
		string(taxStatus), postgres.NullableTime(inv.TaxDeferredAt), inv.TaxRetryCount, inv.TaxPendingReason,
		postgres.NullableString(string(inv.BillingReason)),
		postgres.NullableString(inv.StripeInvoiceID),
		postgres.NullableTime(inv.PaidAt), postgres.NullableTime(inv.VoidedAt),
	).Scan(scanInvDest(&inv)...)

	if err != nil {
		if postgres.IsUniqueViolation(err) {
			return domain.Invoice{}, errs.AlreadyExists("billing_period", "invoice already exists for this billing period")
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
				rating_rule_version_id, billing_period_start, billing_period_end, metadata, created_at,
				tax_jurisdiction, tax_code)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)
		`, itemID, inv.ID, tenantID, items[i].LineType, postgres.NullableString(items[i].MeterID),
			items[i].Description, items[i].Quantity, items[i].UnitAmountCents, items[i].AmountCents,
			items[i].TaxRateBP, items[i].TaxAmountCents, items[i].TotalAmountCents,
			items[i].Currency, postgres.NullableString(items[i].PricingMode),
			postgres.NullableString(items[i].RatingRuleVersionID),
			postgres.NullableTime(items[i].BillingPeriodStart), postgres.NullableTime(items[i].BillingPeriodEnd),
			itemMetaJSON, now, items[i].TaxJurisdiction, items[i].TaxCode,
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

// SetTaxTransaction persists the upstream tax_transaction reference
// (Stripe: tx_xxx) after a successful CommitTax. Required by the credit
// note reversal path, which keys the reversal on the original
// transaction id.
func (s *PostgresStore) SetTaxTransaction(ctx context.Context, tenantID, id string, taxTransactionID string) error {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return err
	}
	defer postgres.Rollback(tx)

	_, err = tx.ExecContext(ctx, `UPDATE invoices SET tax_transaction_id = $1, updated_at = $2 WHERE id = $3`,
		taxTransactionID, time.Now().UTC(), id)
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

	// TxBypass crosses tenants for the scheduler sweep; livemode must still
	// be honoured explicitly from ctx (see scheduler fan-out in #13).
	rows, err := tx.QueryContext(ctx, `
		SELECT `+invCols+` FROM invoices
		WHERE auto_charge_pending = TRUE
		  AND payment_status = 'pending'
		  AND status = 'finalized'
		  AND amount_due_cents > 0
		  AND livemode = $1
		ORDER BY created_at ASC
		LIMIT $2
	`, postgres.Livemode(ctx), limit)
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

// ListUnknownPayments returns invoices whose payment_status is 'unknown' and
// whose last update is older than `olderThan` — the reconciler's cooling
// window before querying Stripe for the authoritative outcome.
func (s *PostgresStore) ListUnknownPayments(ctx context.Context, olderThan time.Time, limit int) ([]domain.Invoice, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	if limit <= 0 {
		limit = 50
	}

	// TxBypass crosses tenants for reconciler sweep; livemode filter prevents
	// test-mode unknowns from being reconciled under live ctx (see #13).
	rows, err := tx.QueryContext(ctx, `
		SELECT `+invCols+` FROM invoices
		WHERE payment_status = 'unknown'
		  AND updated_at < $1
		  AND livemode = $2
		ORDER BY updated_at ASC
		LIMIT $3
	`, olderThan, postgres.Livemode(ctx), limit)
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
		&inv.SourceSubscriptionItemID, (*string)(&inv.SourceChangeType),
		&inv.TaxProvider, &inv.TaxCalculationID, &inv.TaxTransactionID,
		&inv.TaxReverseCharge, &inv.TaxExemptReason,
		(*string)(&inv.TaxStatus), &inv.TaxDeferredAt, &inv.TaxRetryCount, &inv.TaxPendingReason,
		&inv.PublicToken, (*string)(&inv.BillingReason), &inv.StripeInvoiceID,
	}
}

// SetPublicToken writes (or overwrites) the hosted-invoice-URL token for a
// non-draft invoice. The token is the URL — non-guessable by design — so a
// rotation just swaps the column value. Drafts never carry a token, hence
// the status guard; callers that try to set one on a draft get ErrNotFound.
func (s *PostgresStore) SetPublicToken(ctx context.Context, tenantID, invoiceID, token string) error {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return err
	}
	defer postgres.Rollback(tx)

	res, err := tx.ExecContext(ctx, `
		UPDATE invoices SET public_token = $1, updated_at = $2
		WHERE id = $3 AND status <> 'draft'
	`, token, time.Now().UTC(), invoiceID)
	if err != nil {
		if postgres.IsUniqueViolation(err) {
			// 256 bits of entropy means collisions are astronomically unlikely,
			// but if one ever surfaces we want loud failure, not silent reuse
			// of another invoice's URL.
			return fmt.Errorf("set public token: collision: %w", err)
		}
		return fmt.Errorf("set public token: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errs.ErrNotFound
	}
	return tx.Commit()
}

// GetByPublicToken resolves a hosted-invoice-URL token to its invoice row.
// The caller is unauthenticated (a public /invoice/{token} route), so we
// have to look up the tenant FROM the token before any tenant context can
// exist. Runs under TxBypass for exactly that reason; the token's 256 bits
// of entropy plus the UNIQUE index make cross-tenant probing infeasible.
// Empty token returns ErrNotFound rather than querying a null match.
func (s *PostgresStore) GetByPublicToken(ctx context.Context, token string) (domain.Invoice, error) {
	if token == "" {
		return domain.Invoice{}, errs.ErrNotFound
	}
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return domain.Invoice{}, err
	}
	defer postgres.Rollback(tx)

	var inv domain.Invoice
	err = tx.QueryRowContext(ctx, `SELECT `+invCols+` FROM invoices WHERE public_token = $1`, token).
		Scan(scanInvDest(&inv)...)
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

// GetByStripeInvoiceID resolves a Stripe invoice id (in_xxx) to its imported
// Velox invoice row. Backs the velox-import CLI's idempotency lookup —
// re-running an import after a finalized invoice has already landed must
// detect the existing row and emit skip-equivalent (or skip-divergent)
// rather than ErrAlreadyExists from a unique-violation collision.
//
// Empty stripeInvoiceID returns ErrNotFound rather than matching the
// partial unique index's NULL gap (no Velox-native invoice should match).
// Runs under TxTenant — the importer always has a tenant in context, and
// scoping by tenant is the standard RLS posture for this store.
func (s *PostgresStore) GetByStripeInvoiceID(ctx context.Context, tenantID, stripeInvoiceID string) (domain.Invoice, error) {
	if stripeInvoiceID == "" {
		return domain.Invoice{}, errs.ErrNotFound
	}
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Invoice{}, err
	}
	defer postgres.Rollback(tx)

	var inv domain.Invoice
	err = tx.QueryRowContext(ctx, `SELECT `+invCols+` FROM invoices WHERE stripe_invoice_id = $1`, stripeInvoiceID).
		Scan(scanInvDest(&inv)...)
	if err == sql.ErrNoRows {
		return domain.Invoice{}, errs.ErrNotFound
	}
	return inv, err
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
