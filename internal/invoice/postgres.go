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
	"github.com/sagarsuperuser/velox/internal/platform/clock"
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
	COALESCE(tax_error_code,''), tax_next_retry_at,
	COALESCE(payment_card_brand,''), COALESCE(payment_card_last4,''),
	COALESCE(public_token,''), COALESCE(billing_reason,''), COALESCE(stripe_invoice_id,'')`

// qualifiedInvCols returns invCols with every column reference prefixed
// by the given table alias. Used by ADR-029's per-clock queries that
// JOIN invoices to subscriptions — without qualification, columns like
// `id` and `tenant_id` are ambiguous (both tables have them) and
// Postgres rejects the query with SQLSTATE 42702.
//
// Mirrors qualifiedSubCols in internal/subscription/postgres.go;
// kept package-local to keep invCols a single source of truth.
func qualifiedInvCols(alias string) string {
	var b strings.Builder
	for i, col := range splitTopLevelCommas(invCols) {
		if i > 0 {
			b.WriteString(", ")
		}
		col = strings.TrimSpace(col)
		if strings.HasPrefix(col, "COALESCE(") {
			closing := strings.IndexByte(col, ')')
			inner := col[len("COALESCE("):closing]
			parts := strings.SplitN(inner, ",", 2)
			b.WriteString("COALESCE(")
			b.WriteString(alias)
			b.WriteByte('.')
			b.WriteString(strings.TrimSpace(parts[0]))
			if len(parts) == 2 {
				b.WriteString(",")
				b.WriteString(parts[1])
			}
			b.WriteString(col[closing:])
			continue
		}
		b.WriteString(alias)
		b.WriteByte('.')
		b.WriteString(col)
	}
	return b.String()
}

// splitTopLevelCommas splits a column list on commas that are NOT
// inside parentheses (so COALESCE(a, '') stays as one column).
func splitTopLevelCommas(s string) []string {
	var out []string
	depth := 0
	start := 0
	for i, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	out = append(out, s[start:])
	return out
}

func (s *PostgresStore) Create(ctx context.Context, tenantID string, inv domain.Invoice) (domain.Invoice, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Invoice{}, err
	}
	defer postgres.Rollback(tx)

	id := postgres.NewID("vlx_inv")
	// Honor caller-provided CreatedAt — engine paths pass clk.Now()
	// so test-clock-driven invoices align with their issued_at /
	// due_at on simulation time. Zero falls back to wall-clock.
	now := inv.CreatedAt
	if now.IsZero() {
		now = clock.Now(ctx)
	}
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
			tax_status, tax_deferred_at, tax_retry_count, tax_pending_reason, tax_error_code, billing_reason,
			stripe_invoice_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$27,$28,$29,$30,$31,$32,$33,$34,$35,$36,$37,$38,$39,$40,$41)
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
		postgres.NullableString(inv.TaxErrorCode),
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

	// Sort with deterministic tie-break on id. Without the tie-break,
	// catchup-generated invoices share microsecond-level created_at
	// values and Postgres returns ties in arbitrary order — looks
	// "random" to operators. Tie-break direction matches the primary
	// sort direction so consecutive ties read as a single ordered
	// group rather than zig-zagging.
	orderBy := invoiceOrderBy(filter.Sort, filter.SortDir)
	query := `SELECT ` + invCols + ` FROM invoices` + where +
		` ORDER BY ` + orderBy +
		` LIMIT $` + fmt.Sprintf("%d", len(args)+1) +
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

	now := clock.Now(ctx)
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

	now := clock.Now(ctx)
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

	now := clock.Now(ctx)
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

// SetPaymentCard stamps the card brand + last4 used to settle an
// invoice. Called from the payment_intent.succeeded webhook handler
// AFTER MarkPaid lands; kept as a separate update so MarkPaid stays
// backward-compatible (many call sites — dunning retrier, public
// payment-page handler, payment reconciler — none of which know
// about the card). Best-effort: card resolution failure leaves the
// columns empty, which renders no sub-line in the timeline.
// Migration 0077 / ADR-020.
func (s *PostgresStore) SetPaymentCard(ctx context.Context, tenantID, id, brand, last4 string) error {
	if brand == "" && last4 == "" {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return err
	}
	defer postgres.Rollback(tx)

	res, err := tx.ExecContext(ctx, `
		UPDATE invoices SET
			payment_card_brand = $1,
			payment_card_last4 = $2,
			updated_at = now()
		WHERE id = $3
	`, postgres.NullableString(brand), postgres.NullableString(last4), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errs.ErrNotFound
	}
	return tx.Commit()
}

func (s *PostgresStore) ApplyCreditNote(ctx context.Context, tenantID, id string, amountCents int64) (domain.Invoice, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Invoice{}, err
	}
	defer postgres.Rollback(tx)

	now := clock.Now(ctx)
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

	now := clock.Now(ctx)
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

	now := clock.Now(ctx)
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
	now := clock.Now(ctx)
	metaJSON, _ := json.Marshal(item.Metadata)
	if item.Metadata == nil {
		metaJSON = []byte("{}")
	}

	err = tx.QueryRowContext(ctx, `
		INSERT INTO invoice_line_items (id, invoice_id, tenant_id, line_type, meter_id,
			description, quantity, unit_amount_cents, amount_cents, tax_rate_bp, tax_amount_cents,
			total_amount_cents, currency, pricing_mode, rating_rule_version_id,
			billing_period_start, billing_period_end, metadata, created_at,
			tax_jurisdiction, tax_code, tax_reason)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22)
		RETURNING id, invoice_id, tenant_id, line_type, COALESCE(meter_id,''), description,
			quantity, unit_amount_cents, amount_cents, tax_rate_bp, tax_amount_cents,
			total_amount_cents, currency, COALESCE(pricing_mode,''),
			COALESCE(rating_rule_version_id,''), billing_period_start, billing_period_end,
			metadata, created_at, tax_jurisdiction, tax_code, tax_reason
	`, id, item.InvoiceID, tenantID, item.LineType, postgres.NullableString(item.MeterID),
		item.Description, item.Quantity, item.UnitAmountCents, item.AmountCents,
		item.TaxRateBP, item.TaxAmountCents, item.TotalAmountCents, item.Currency,
		postgres.NullableString(item.PricingMode), postgres.NullableString(item.RatingRuleVersionID),
		postgres.NullableTime(item.BillingPeriodStart), postgres.NullableTime(item.BillingPeriodEnd),
		metaJSON, now, item.TaxJurisdiction, item.TaxCode, item.TaxabilityReason,
	).Scan(&item.ID, &item.InvoiceID, &item.TenantID, &item.LineType, &item.MeterID,
		&item.Description, &item.Quantity, &item.UnitAmountCents, &item.AmountCents,
		&item.TaxRateBP, &item.TaxAmountCents, &item.TotalAmountCents, &item.Currency,
		&item.PricingMode, &item.RatingRuleVersionID,
		&item.BillingPeriodStart, &item.BillingPeriodEnd, &metaJSON, &item.CreatedAt,
		&item.TaxJurisdiction, &item.TaxCode, &item.TaxabilityReason)

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
			metadata, created_at, tax_jurisdiction, tax_code, tax_reason
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
			&item.TaxJurisdiction, &item.TaxCode, &item.TaxabilityReason); err != nil {
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

// ListApproachingDue — CRON path. ADR-029 Phase 6: clock-pinned
// invoices are excluded; the catchup orchestrator drives per-clock
// reminder dispatch via ListApproachingDueForClock against the clock's
// frozen_time. Without this filter, the wall-clock cron would email
// reminders for clock-pinned invoices "due 3 days from wall-clock now"
// — but those invoices' due_at is in simulated time, so the email
// would either fire never (simulated due is in 2027) or wrong-cadence.
func (s *PostgresStore) ListApproachingDue(ctx context.Context, daysBeforeDue int) ([]domain.Invoice, error) {
	// Cross-tenant query for the billing scheduler — bypass RLS.
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	rows, err := tx.QueryContext(ctx, `
		SELECT `+invCols+` FROM invoices i
		WHERE i.status = 'finalized'
		  AND i.payment_status IN ('pending', 'failed')
		  AND i.due_at IS NOT NULL
		  AND i.due_at BETWEEN NOW() AND NOW() + INTERVAL '1 day' * $1
		  AND i.amount_due_cents > 0
		  AND i.livemode = $2
		  AND NOT EXISTS (
		    SELECT 1 FROM subscriptions s
		    WHERE s.id = i.subscription_id
		      AND s.test_clock_id IS NOT NULL
		  )
		ORDER BY i.due_at ASC
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

// ListApproachingDueForClock is the catchup-path counterpart to
// ListApproachingDue. ADR-029 Phase 6 — returns clock-pinned invoices
// whose due_at falls within `daysBeforeDue` of the clock's frozen_time
// (passed explicitly so the comparison runs in simulated time).
func (s *PostgresStore) ListApproachingDueForClock(ctx context.Context, tenantID, clockID string, frozenTime time.Time, daysBeforeDue int) ([]domain.Invoice, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	rows, err := tx.QueryContext(ctx, `
		SELECT `+qualifiedInvCols("i")+` FROM invoices i
		JOIN subscriptions s ON s.id = i.subscription_id
		WHERE i.tenant_id = $1
		  AND s.test_clock_id = $2
		  AND i.status = 'finalized'
		  AND i.payment_status IN ('pending', 'failed')
		  AND i.due_at IS NOT NULL
		  AND i.due_at BETWEEN $3 AND $3 + INTERVAL '1 day' * $4
		  AND i.amount_due_cents > 0
		ORDER BY i.due_at ASC
		LIMIT 500
	`, tenantID, clockID, frozenTime, daysBeforeDue)
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
	now := clock.Now(ctx)
	metaJSON, _ := json.Marshal(item.Metadata)
	if item.Metadata == nil {
		metaJSON = []byte("{}")
	}

	err = tx.QueryRowContext(ctx, `
		INSERT INTO invoice_line_items (id, invoice_id, tenant_id, line_type, meter_id,
			description, quantity, unit_amount_cents, amount_cents, tax_rate_bp, tax_amount_cents,
			total_amount_cents, currency, pricing_mode, rating_rule_version_id,
			billing_period_start, billing_period_end, metadata, created_at,
			tax_jurisdiction, tax_code, tax_reason)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22)
		RETURNING id, invoice_id, tenant_id, line_type, COALESCE(meter_id,''), description,
			quantity, unit_amount_cents, amount_cents, tax_rate_bp, tax_amount_cents,
			total_amount_cents, currency, COALESCE(pricing_mode,''),
			COALESCE(rating_rule_version_id,''), billing_period_start, billing_period_end,
			metadata, created_at, tax_jurisdiction, tax_code, tax_reason
	`, itemID, invoiceID, tenantID, item.LineType, postgres.NullableString(item.MeterID),
		item.Description, item.Quantity, item.UnitAmountCents, item.AmountCents,
		item.TaxRateBP, item.TaxAmountCents, item.TotalAmountCents, currency,
		postgres.NullableString(item.PricingMode), postgres.NullableString(item.RatingRuleVersionID),
		postgres.NullableTime(item.BillingPeriodStart), postgres.NullableTime(item.BillingPeriodEnd),
		metaJSON, now, item.TaxJurisdiction, item.TaxCode, item.TaxabilityReason,
	).Scan(&item.ID, &item.InvoiceID, &item.TenantID, &item.LineType, &item.MeterID,
		&item.Description, &item.Quantity, &item.UnitAmountCents, &item.AmountCents,
		&item.TaxRateBP, &item.TaxAmountCents, &item.TotalAmountCents, &item.Currency,
		&item.PricingMode, &item.RatingRuleVersionID,
		&item.BillingPeriodStart, &item.BillingPeriodEnd, &metaJSON, &item.CreatedAt,
		&item.TaxJurisdiction, &item.TaxCode, &item.TaxabilityReason)
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

	now := clock.Now(ctx)

	for _, li := range lineItems {
		if li.ID == "" {
			continue
		}
		_, err = tx.ExecContext(ctx, `
			UPDATE invoice_line_items
			SET amount_cents = $3,
			    tax_rate_bp = $4,
			    tax_amount_cents = $5,
			    total_amount_cents = $6,
			    tax_reason = $7
			WHERE invoice_id = $1 AND id = $2
		`, invoiceID, li.ID, li.AmountCents, li.TaxRateBP, li.TaxAmountCents, li.TotalAmountCents, li.TaxabilityReason)
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
			tax_error_code = $16,
			total_amount_cents = $17,
			amount_due_cents = GREATEST($17 - amount_paid_cents - credits_applied_cents, 0),
			updated_at = $18
		WHERE id = $1
		RETURNING `+invCols,
		invoiceID,
		update.SubtotalCents, update.DiscountCents, update.TaxAmountCents, update.TaxRateBP,
		update.TaxName, update.TaxCountry, update.TaxID,
		// tax_provider + tax_calculation_id are NOT NULL DEFAULT ''
		// — pass empty string directly. NullableString would
		// convert "" → SQLNULL → constraint violation. Bug surfaced
		// 2026-05-04 when tax retry on orphan invoices wrote empty
		// calculation_ids (none/manual providers don't issue them)
		// and tripped SQLSTATE 23502.
		update.TaxProvider,
		update.TaxCalculationID,
		update.TaxReverseCharge, update.TaxExemptReason,
		update.TaxStatus, postgres.NullableTime(update.TaxDeferredAt), update.TaxPendingReason,
		postgres.NullableString(update.TaxErrorCode),
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

// UpdateTaxAtomic re-stamps an invoice's tax decision after a manual
// retry (or, eventually, the retry worker). Locks the invoice row FOR
// UPDATE, gates on tax_status in (pending, failed) and status='draft',
// rewrites per-line tax fields from the supplied lineItems, rewrites
// the invoice-level tax columns, recomputes total / amount_due, and
// increments tax_retry_count. Returns the updated row with attention
// re-derivable from the new fields.
//
// A retry that succeeds writes tax_status='ok' with calculation_id
// populated and tax_pending_reason / tax_error_code cleared. A retry
// that fails again writes tax_status='pending' or 'failed' with the
// new code/reason — the row stays blocked from finalize and the
// dashboard banner refreshes.
func (s *PostgresStore) UpdateTaxAtomic(
	ctx context.Context, tenantID, invoiceID string,
	update domain.InvoiceTaxRetryUpdate, lineItems []domain.InvoiceLineItem,
) (domain.Invoice, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Invoice{}, err
	}
	defer postgres.Rollback(tx)

	var (
		status    domain.InvoiceStatus
		taxStatus string
	)
	err = tx.QueryRowContext(ctx,
		`SELECT status, tax_status FROM invoices WHERE id = $1 FOR UPDATE`,
		invoiceID,
	).Scan(&status, &taxStatus)
	if err == sql.ErrNoRows {
		return domain.Invoice{}, errs.ErrNotFound
	}
	if err != nil {
		return domain.Invoice{}, err
	}
	if status != domain.InvoiceDraft {
		return domain.Invoice{}, errs.InvalidState(fmt.Sprintf(
			"tax retry only valid on draft invoices (current: %s)", status))
	}
	if taxStatus != string(domain.InvoiceTaxPending) && taxStatus != string(domain.InvoiceTaxFailed) {
		return domain.Invoice{}, errs.InvalidState(fmt.Sprintf(
			"tax retry only valid when tax_status in (pending, failed) (current: %s)", taxStatus))
	}

	now := clock.Now(ctx)

	for _, li := range lineItems {
		if li.ID == "" {
			continue
		}
		_, err = tx.ExecContext(ctx, `
			UPDATE invoice_line_items
			SET tax_rate_bp = $3,
			    tax_amount_cents = $4,
			    total_amount_cents = $5,
			    tax_jurisdiction = $6,
			    tax_code = $7,
			    tax_reason = $8
			WHERE invoice_id = $1 AND id = $2
		`, invoiceID, li.ID, li.TaxRateBP, li.TaxAmountCents, li.TotalAmountCents,
			li.TaxJurisdiction, li.TaxCode, li.TaxabilityReason)
		if err != nil {
			return domain.Invoice{}, fmt.Errorf("update line item tax stamp: %w", err)
		}
	}

	var inv domain.Invoice
	err = tx.QueryRowContext(ctx, `
		UPDATE invoices SET
			tax_amount_cents = $2,
			tax_rate_bp = $3,
			tax_name = $4,
			tax_country = $5,
			tax_id = $6,
			tax_provider = $7,
			tax_calculation_id = $8,
			tax_reverse_charge = $9,
			tax_exempt_reason = $10,
			tax_status = $11,
			tax_deferred_at = $12,
			tax_pending_reason = $13,
			tax_error_code = $14,
			tax_retry_count = tax_retry_count + 1,
			tax_next_retry_at = $15,
			total_amount_cents = $16,
			amount_due_cents = GREATEST($16 - amount_paid_cents - credits_applied_cents, 0),
			updated_at = $17
		WHERE id = $1
		RETURNING `+invCols,
		invoiceID,
		update.TaxAmountCents, update.TaxRateBP,
		update.TaxName, update.TaxCountry, update.TaxID,
		postgres.NullableString(update.TaxProvider),
		postgres.NullableString(update.TaxCalculationID),
		update.TaxReverseCharge, update.TaxExemptReason,
		string(update.TaxStatus), postgres.NullableTime(update.TaxDeferredAt),
		update.TaxPendingReason, postgres.NullableString(update.TaxErrorCode),
		postgres.NullableTime(update.TaxNextRetryAt),
		update.TotalAmountCents, now,
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
	// Honor caller-provided CreatedAt — engine paths pass clk.Now()
	// so test-clock-driven invoices align with their issued_at /
	// due_at on simulation time. Zero falls back to wall-clock.
	now := inv.CreatedAt
	if now.IsZero() {
		now = clock.Now(ctx)
	}
	metaJSON, _ := json.Marshal(inv.Metadata)
	if inv.Metadata == nil {
		metaJSON = []byte("{}")
	}

	taxStatus := inv.TaxStatus
	if taxStatus == "" {
		taxStatus = domain.InvoiceTaxOK
	}
	// Public token is a property of the finalized state. Service.Finalize
	// mints one for the operator-driven path; the billing engine
	// (engine.go + threshold_scan.go) inserts directly with status=
	// finalized and previously skipped the mint, leaving every
	// engine-generated invoice without a hosted_invoice_url. That breaks
	// every customer-facing email CTA. Mint here so the invariant
	// "finalized ⇒ has public_token" holds at the data boundary
	// regardless of which caller produced the invoice. A generation
	// failure is non-fatal — the row still inserts; operators can
	// repair via the rotate endpoint.
	publicToken := inv.PublicToken
	if publicToken == "" && inv.Status == domain.InvoiceFinalized {
		if t, tokenErr := GeneratePublicToken(); tokenErr == nil {
			publicToken = t
		}
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
			tax_status, tax_deferred_at, tax_retry_count, tax_pending_reason, tax_error_code, billing_reason,
			stripe_invoice_id, public_token, paid_at, voided_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$28,$29,$30,$31,$32,$33,$34,$35,$36,$37,$38,$39,$40,$41,$42,$43,$44,$45)
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
		postgres.NullableString(inv.TaxErrorCode),
		postgres.NullableString(string(inv.BillingReason)),
		postgres.NullableString(inv.StripeInvoiceID),
		postgres.NullableString(publicToken),
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
				tax_jurisdiction, tax_code, tax_reason)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22)
		`, itemID, inv.ID, tenantID, items[i].LineType, postgres.NullableString(items[i].MeterID),
			items[i].Description, items[i].Quantity, items[i].UnitAmountCents, items[i].AmountCents,
			items[i].TaxRateBP, items[i].TaxAmountCents, items[i].TotalAmountCents,
			items[i].Currency, postgres.NullableString(items[i].PricingMode),
			postgres.NullableString(items[i].RatingRuleVersionID),
			postgres.NullableTime(items[i].BillingPeriodStart), postgres.NullableTime(items[i].BillingPeriodEnd),
			itemMetaJSON, now, items[i].TaxJurisdiction, items[i].TaxCode, items[i].TaxabilityReason,
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
		pending, clock.Now(ctx), id)
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
		taxTransactionID, clock.Now(ctx), id)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// ListAutoChargePending returns invoices that need auto-charge retry —
// CRON path. Excludes clock-pinned subscriptions per ADR-029: simulation
// time progresses only on operator Advance, so the wall-clock scheduler
// must never charge a clock-pinned invoice. The catchup worker uses
// ListAutoChargePendingForClock as the disjoint per-clock entry point.
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
	//
	// The NOT EXISTS clock-exclusion uses the subscriptions JOIN target
	// (not invoices.subscription_id directly) so we exclude only invoices
	// whose owning sub is clock-pinned. One-off invoices (subscription_id
	// is empty / unknown) fall through and remain cron-eligible — they
	// don't have a sub to be clock-pinned to.
	rows, err := tx.QueryContext(ctx, `
		SELECT `+invCols+` FROM invoices i
		WHERE i.auto_charge_pending = TRUE
		  AND i.payment_status = 'pending'
		  AND i.status = 'finalized'
		  AND i.amount_due_cents > 0
		  AND i.livemode = $1
		  AND NOT EXISTS (
		    SELECT 1 FROM subscriptions s
		    WHERE s.id = i.subscription_id
		      AND s.test_clock_id IS NOT NULL
		  )
		ORDER BY i.created_at ASC
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

// ListAutoChargePendingForClock is the catchup-path counterpart to
// ListAutoChargePending. Returns invoices whose owning subscription is
// pinned to the given clock and need auto-charge retry. ADR-029 Phase 1.
//
// Time predicate is implicit: the catchup worker calls this AFTER it
// has finalized any newly-due periods for the same clock, so any
// auto_charge_pending row that exists is by definition due for a
// retry attempt. No separate "due_at" filter — cycle math is owned by
// the period-generation phase.
//
// Scoped by tenantID + clockID; livemode is implied (test clocks are
// test-mode-only, enforced by the test_clocks CHECK constraint).
func (s *PostgresStore) ListAutoChargePendingForClock(ctx context.Context, tenantID, clockID string, limit int) ([]domain.Invoice, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	if limit <= 0 {
		limit = 50
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT `+qualifiedInvCols("i")+` FROM invoices i
		JOIN subscriptions s ON s.id = i.subscription_id
		WHERE i.auto_charge_pending = TRUE
		  AND i.payment_status = 'pending'
		  AND i.status = 'finalized'
		  AND i.amount_due_cents > 0
		  AND i.tenant_id = $1
		  AND s.test_clock_id = $2
		ORDER BY i.created_at ASC
		LIMIT $3
	`, tenantID, clockID, limit)
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

// ListPendingTaxRetry powers the background tax-retry reconciler
// (ADR-017). RLS-bypassed because the sweeper crosses tenants;
// returned rows carry their tenant_id so the caller dispatches
// per-row under the correct RLS partition.
//
// Filter:
//   - status='draft' AND tax_status IN (pending, failed)
//   - tax_error_code IN retryableCodes (e.g. provider_outage,
//     unknown). Empty list short-circuits to "none".
//   - tax_retry_count < maxAttempts (per-invoice cap)
//   - tax_next_retry_at IS NULL OR tax_next_retry_at <= now()
//
// Postgres uses idx_invoices_tax_retry_due (migration 0074) to
// narrow the scan; the predicate matches the index where clause
// exactly so the planner picks it.
// ListPendingTaxRetry — CRON path. ADR-029 Phase 2: clock-pinned
// invoices are excluded; the test-clock catchup orchestrator drives
// tax retry for clock-pinned subs via ListPendingTaxRetryForClock.
// Without this filter the wall-clock scheduler would retry tax on a
// clock-pinned invoice every tick — same drip-bill smell ADR-028
// closed for period generation.
func (s *PostgresStore) ListPendingTaxRetry(ctx context.Context, batch int, retryableCodes []string, maxAttempts int, livemode bool) ([]domain.Invoice, error) {
	if batch <= 0 {
		batch = 50
	}
	if len(retryableCodes) == 0 {
		return nil, nil
	}
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	rows, err := tx.QueryContext(ctx, `
		SELECT `+invCols+` FROM invoices i
		WHERE i.livemode = $1
		  AND i.status = 'draft'
		  AND i.tax_status IN ('pending', 'failed')
		  AND COALESCE(i.tax_error_code, '') = ANY($2)
		  AND i.tax_retry_count < $3
		  AND (i.tax_next_retry_at IS NULL OR i.tax_next_retry_at <= now())
		  AND NOT EXISTS (
		    SELECT 1 FROM subscriptions s
		    WHERE s.id = i.subscription_id
		      AND s.test_clock_id IS NOT NULL
		  )
		ORDER BY i.tax_next_retry_at ASC NULLS FIRST
		LIMIT $4
	`, livemode, postgres.StringArray(retryableCodes), maxAttempts, batch)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []domain.Invoice
	for rows.Next() {
		var inv domain.Invoice
		if err := rows.Scan(scanInvDest(&inv)...); err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

// ListPendingTaxRetryForClock is the catchup-path counterpart to
// ListPendingTaxRetry. ADR-029 Phase 2.
//
// Differences from the cron path:
//   - Scoped to (tenant, clock) — clock-pinned subs only.
//   - No `tax_next_retry_at <= now()` predicate. The catchup
//     orchestrator drives this exactly once per Advance, so backoff
//     scheduling against simulated instants would silently no-op
//     small advances and confuse operators (they click Advance
//     expecting "one shot per pending row"). The retry-count cap
//     (maxAttempts) still applies as a defense against runaway
//     retries chewing through the 10-min catchup ceiling.
//
// Design choice (operator-friendly over production-fidelity): each
// Advance gives every pending row exactly one retry attempt. An
// operator running through a tax-error rehearsal scenario clicks
// Advance, sees count go up by 1 per row, and can predict the
// behaviour without doing backoff arithmetic. Faithful per-window
// retry-sequence simulation (Stripe-parity event-walking) is
// deferred to a follow-up ADR — it's a niche use case operators
// don't typically run, while operator-confusion from "I clicked
// Advance and nothing happened" is a daily failure mode.
func (s *PostgresStore) ListPendingTaxRetryForClock(ctx context.Context, tenantID, clockID string, retryableCodes []string, maxAttempts, limit int) ([]domain.Invoice, error) {
	if limit <= 0 {
		limit = 50
	}
	if len(retryableCodes) == 0 {
		return nil, nil
	}
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	rows, err := tx.QueryContext(ctx, `
		SELECT `+qualifiedInvCols("i")+` FROM invoices i
		JOIN subscriptions s ON s.id = i.subscription_id
		WHERE i.tenant_id = $1
		  AND s.test_clock_id = $2
		  AND i.status = 'draft'
		  AND i.tax_status IN ('pending', 'failed')
		  AND COALESCE(i.tax_error_code, '') = ANY($3)
		  AND i.tax_retry_count < $4
		ORDER BY i.created_at ASC
		LIMIT $5
	`, tenantID, clockID, postgres.StringArray(retryableCodes), maxAttempts, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []domain.Invoice
	for rows.Next() {
		var inv domain.Invoice
		if err := rows.Scan(scanInvDest(&inv)...); err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

// ListProviderConfigErrors returns draft invoices stuck on Stripe-
// configuration tax errors (provider_not_configured / provider_auth)
// for one (tenant, livemode). Backs the
// tenantstripe.Connect → invoice.Service.RetryProviderConfigErrors
// fan-out per ADR-019. Tenant-scoped via TxTenant + WithLivemode on
// the request ctx; the per-mode filter is also explicit in the WHERE
// so a misconfigured ctx can't accidentally surface the wrong mode's
// rows.
func (s *PostgresStore) ListProviderConfigErrors(ctx context.Context, tenantID string, livemode bool) ([]domain.Invoice, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	rows, err := tx.QueryContext(ctx, `
		SELECT `+invCols+` FROM invoices
		WHERE livemode = $1
		  AND status = 'draft'
		  AND tax_status IN ('pending', 'failed')
		  AND COALESCE(tax_error_code, '') IN ('provider_not_configured', 'provider_auth')
		ORDER BY created_at ASC
		LIMIT 1000
	`, livemode)
	return s.scanInvoiceRows(rows, err)
}

// ListCustomerDataInvalidErrors returns draft invoices for ONE customer
// stuck on `customer_data_invalid` — the only tax error a billing-
// profile update can resolve. Mirrors ListProviderConfigErrors but
// scoped to a customer instead of a (tenant, livemode) — fired after
// the operator updates a customer's address/postal/state/tax_id so
// any of that customer's stuck invoices auto-retry without
// per-invoice clicking. Same surgical-filter principle as ADR-019.
func (s *PostgresStore) ListCustomerDataInvalidErrors(ctx context.Context, tenantID, customerID string) ([]domain.Invoice, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	rows, err := tx.QueryContext(ctx, `
		SELECT `+invCols+` FROM invoices
		WHERE customer_id = $1
		  AND status = 'draft'
		  AND tax_status IN ('pending', 'failed')
		  AND COALESCE(tax_error_code, '') = 'customer_data_invalid'
		ORDER BY created_at ASC
		LIMIT 1000
	`, customerID)
	return s.scanInvoiceRows(rows, err)
}

// scanInvoiceRows is the shared per-row scanning body of the two
// retry-fanout list queries above. Centralized so the close-on-error
// + scan loop don't drift between the two callers.
func (s *PostgresStore) scanInvoiceRows(rows *sql.Rows, queryErr error) ([]domain.Invoice, error) {
	if queryErr != nil {
		return nil, queryErr
	}
	defer func() { _ = rows.Close() }()

	var out []domain.Invoice
	for rows.Next() {
		var inv domain.Invoice
		if err := rows.Scan(scanInvDest(&inv)...); err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	return out, rows.Err()
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
		&inv.TaxErrorCode, &inv.TaxNextRetryAt,
		&inv.PaymentCardBrand, &inv.PaymentCardLast4,
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
	`, token, clock.Now(ctx), invoiceID)
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
func (s *PostgresStore) GetByPublicToken(ctx context.Context, token string) (domain.Invoice, bool, error) {
	if token == "" {
		return domain.Invoice{}, false, errs.ErrNotFound
	}
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return domain.Invoice{}, false, err
	}
	defer postgres.Rollback(tx)

	var (
		inv      domain.Invoice
		livemode bool
	)
	dest := append(scanInvDest(&inv), &livemode)
	err = tx.QueryRowContext(ctx, `SELECT `+invCols+`, livemode FROM invoices WHERE public_token = $1`, token).
		Scan(dest...)
	if err == sql.ErrNoRows {
		return domain.Invoice{}, false, errs.ErrNotFound
	}
	if err != nil {
		return domain.Invoice{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Invoice{}, false, err
	}
	return inv, livemode, nil
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

// invoiceOrderBy returns the ORDER BY clause for the invoice list,
// validating sort + dir against a closed set. Anything outside the
// allow-list silently falls back to the default — never interpolate
// raw user input into SQL.
//
// Tie-break on id matches the primary sort direction so a sequence
// of ties reads as a single ordered group rather than zig-zagging.
// The id column is monotonic (ulid-prefixed); using it as the
// secondary sort gives a stable, deterministic order.
func invoiceOrderBy(sort, dir string) string {
	col := invoiceSortColumn(sort)
	d := "DESC"
	if dir == "asc" {
		d = "ASC"
	}
	return col + " " + d + ", id " + d
}

// invoiceSortColumn maps the SPA's sort key to a SQL column name.
// Closed allow-list to prevent injection. Unknown keys default to
// created_at (the most common sort, matches Stripe Dashboard).
func invoiceSortColumn(key string) string {
	switch key {
	case "invoice_number":
		return "invoice_number"
	case "amount_due_cents", "amount_due":
		return "amount_due_cents"
	case "billing_period_start", "period":
		return "billing_period_start"
	case "due_at":
		return "due_at"
	case "issued_at":
		return "issued_at"
	case "status":
		return "status"
	case "payment_status":
		return "payment_status"
	default:
		return "created_at"
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
