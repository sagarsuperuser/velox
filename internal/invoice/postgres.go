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
	"github.com/sagarsuperuser/velox/internal/platform/crypto"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

type PostgresStore struct {
	db     *postgres.DB
	enc    *crypto.Encryptor
	outbox OutboxEnqueuer
}

func NewPostgresStore(db *postgres.DB) *PostgresStore {
	return &PostgresStore{db: db, enc: crypto.NewNoop()}
}

// OutboxEnqueuer enqueues an outbound webhook event inside the caller's tx, so
// the event is persisted atomically with the state change (ADR-040 transactional
// outbox). Satisfied by *webhook.OutboxStore; declared as a narrow consumer-side
// interface so the invoice store needs no webhook import.
type OutboxEnqueuer interface {
	Enqueue(ctx context.Context, tx *sql.Tx, tenantID, eventType string, payload map[string]any) (string, error)
}

// SetOutboxEnqueuer wires transactional webhook emission for transitions that
// fire from many call sites (notably invoice.paid, emitted from MarkPaid so it
// fires exactly once regardless of which path settled the invoice). Optional —
// when unset, no event is enqueued.
func (s *PostgresStore) SetOutboxEnqueuer(o OutboxEnqueuer) { s.outbox = o }

// SetEncryptor wires AES-256-GCM encryption for the hosted-invoice public_token
// at rest. When set (non-noop), the raw token is encrypted before storage and
// decrypted on read so the hosted URL can be rebuilt on re-send; lookups go
// through the SHA-256 blind index (public_token_hash) which never needs the key.
// Without it, public_token_encrypted holds plaintext (dev/migration parity) —
// the blind index still hides the raw token from a snapshot of the hash column.
func (s *PostgresStore) SetEncryptor(enc *crypto.Encryptor) {
	if enc == nil {
		enc = crypto.NewNoop()
	}
	s.enc = enc
}

// decryptScanner is a sql.Scanner that decrypts the public_token_encrypted
// column into a plain string on read, so inv.PublicToken stays populated for
// the e-mail/checkout-URL paths without those callers knowing about encryption.
type decryptScanner struct {
	enc *crypto.Encryptor
	dst *string
}

// encodeToken returns the (encrypted, hash) pair to persist for a raw hosted-
// invoice token. Empty token → empty pair. Encryption failure (real key
// misconfigured) is non-fatal: the hash is still stored so lookups work; only
// the re-send URL is degraded until the token is rotated.
func (s *PostgresStore) encodeToken(rawToken string) (encrypted, hash string) {
	if rawToken == "" {
		return "", ""
	}
	hash = HashPublicToken(rawToken)
	if ct, err := s.enc.Encrypt(rawToken); err == nil {
		encrypted = ct
	}
	return encrypted, hash
}

func (d decryptScanner) Scan(src any) error {
	var ct string
	switch v := src.(type) {
	case nil:
		*d.dst = ""
		return nil
	case []byte:
		ct = string(v)
	case string:
		ct = v
	default:
		return fmt.Errorf("decryptScanner: unexpected source type %T", src)
	}
	if ct == "" {
		*d.dst = ""
		return nil
	}
	pt, err := d.enc.Decrypt(ct)
	if err != nil {
		return fmt.Errorf("decrypt public_token: %w", err)
	}
	*d.dst = pt
	return nil
}

// Unique-index names on the invoices table. Constants instead of inline
// string literals so the constraint-name router in mapInvoiceUniqueViolation
// is type-checked against actual index names — a typo here surfaces in
// integration tests, not as a silent fall-through to the generic
// "unknown constraint" branch.
const (
	idxInvoicesBillingIdempotency  = "idx_invoices_billing_idempotency"
	idxInvoicesProrationDedup      = "idx_invoices_proration_dedup"
	idxInvoicesInvoiceNumberUnique = "invoices_tenant_id_livemode_invoice_number_key"
	idxInvoicesPublicTokenUnique   = "idx_invoices_public_token_hash"
	idxInvoicesThresholdPerCycle   = "idx_invoices_threshold_unique_per_cycle"
	idxInvoicesStripeInvoiceID     = "idx_invoices_stripe_invoice_id"
)

// mapInvoiceUniqueViolation routes a Postgres unique-violation error to
// a structured DomainError tagged with the constraint that fired. Pre-
// 2026-05-28 every invoice unique violation was squashed into a single
// "billing_period" or "invoice_number" message — callers couldn't tell
// which constraint fired and the proration-retry path mis-routed
// billing-period collisions through GetByProrationSource (which then
// returned "not found" because the existing row had a different item
// ID). See ADR-030 cross-flow audit + feedback_no_silent_fallbacks
// memory.
//
// Returns nil if err isn't a unique violation; caller passes the
// original err through unchanged in that case.
func mapInvoiceUniqueViolation(err error, inv domain.Invoice) error {
	if !postgres.IsUniqueViolation(err) {
		return nil
	}
	switch postgres.UniqueViolationConstraint(err) {
	case idxInvoicesBillingIdempotency:
		return errs.AlreadyExists("billing_period",
			fmt.Sprintf("invoice already exists for subscription %q period %s..%s",
				inv.SubscriptionID,
				inv.BillingPeriodStart.UTC().Format("2006-01-02"),
				inv.BillingPeriodEnd.UTC().Format("2006-01-02"))).WithCode("invoice_billing_period_taken")
	case idxInvoicesProrationDedup:
		return errs.AlreadyExists("proration_source",
			"proration invoice already exists for this item change").WithCode("invoice_proration_source_taken")
	case idxInvoicesInvoiceNumberUnique:
		return errs.AlreadyExists("invoice_number",
			fmt.Sprintf("invoice number %q already exists", inv.InvoiceNumber)).WithCode("invoice_number_taken")
	case idxInvoicesPublicTokenUnique:
		// 256-bit random token collision is astronomically unlikely; if
		// it ever fires, fail loudly rather than re-use the existing
		// row (would expose another invoice via the duplicate URL).
		return errs.AlreadyExists("public_token",
			"public token collision — regenerate").WithCode("invoice_public_token_collision")
	case idxInvoicesThresholdPerCycle:
		return errs.AlreadyExists("threshold_cycle",
			"threshold invoice already exists for this subscription cycle").WithCode("invoice_threshold_cycle_taken")
	case idxInvoicesStripeInvoiceID:
		return errs.AlreadyExists("stripe_invoice_id",
			"a Velox invoice is already linked to this Stripe invoice id").WithCode("invoice_stripe_id_taken")
	}
	// Unknown constraint — surface the constraint name in the message
	// so the operator + logs identify what fired, instead of squashing
	// into a generic "already exists" that the caller misroutes.
	return errs.AlreadyExists("",
		fmt.Sprintf("unique constraint %q violated on invoice insert",
			postgres.UniqueViolationConstraint(err))).WithCode("invoice_unique_violation")
}

const invCols = `id, tenant_id, customer_id, COALESCE(subscription_id,''), invoice_number, status,
	payment_status, currency, subtotal_cents, discount_cents, tax_amount_cents, tax_rate,
	COALESCE(tax_name,''), COALESCE(tax_country,''), COALESCE(tax_id,''),
	total_amount_cents, amount_due_cents, amount_paid_cents, credits_applied_cents,
	billing_period_start, billing_period_end, issued_at, due_at, paid_at, voided_at, uncollectible_at,
	COALESCE(stripe_payment_intent_id,''), COALESCE(last_payment_error,''),
	payment_overdue, auto_charge_pending, net_payment_term_days, COALESCE(memo,''), COALESCE(footer,''),
	metadata, created_at, updated_at, source_plan_changed_at, COALESCE(source_subscription_item_id,''),
	COALESCE(source_change_type,''),
	tax_provider, tax_calculation_id, COALESCE(tax_transaction_id,''),
	tax_reverse_charge, tax_exempt_reason,
	tax_status, tax_deferred_at, tax_retry_count, tax_pending_reason,
	COALESCE(tax_error_code,''), tax_next_retry_at,
	COALESCE(payment_card_brand,''), COALESCE(payment_card_last4,''),
	COALESCE(public_token_encrypted,''), COALESCE(billing_reason,''), COALESCE(stripe_invoice_id,''),
	is_simulated`

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
// inside parentheses (so COALESCE(a, ”) stays as one column).
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

	// Canonical UPPERCASE ISO-4217. This is the single chokepoint both
	// manual (Service.Create) and cycle (engine, incl. its "usd" fallback)
	// invoices funnel through, so the stored currency can't vary by origin.
	// Must match the tenant default ("USD") because analytics/dunning
	// revenue queries filter with a case-sensitive `currency = $1`.
	inv.Currency = strings.ToUpper(inv.Currency)

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
			stripe_invoice_id, is_simulated)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$27,$28,$29,$30,$31,$32,$33,$34,$35,$36,$37,$38,$39,$40,$41,$42)
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
		inv.IsSimulated,
	).Scan(s.scanInvDest(&inv)...)

	if err != nil {
		if mapped := mapInvoiceUniqueViolation(err, inv); mapped != nil {
			return domain.Invoice{}, mapped
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
		Scan(s.scanInvDest(&inv)...)
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
	).Scan(s.scanInvDest(&inv)...)
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
		Scan(s.scanInvDest(&inv)...)
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

	// Limit: default to 50 when unset/invalid; clamp to 100 when caller
	// asks for more. Pre-2026-05-28 the over-cap branch silently fell
	// back to 50 — surprising for any caller asking for >100 (e.g. the
	// CSV export's exportPageSize=1000 only ever got 50 back, then the
	// pagination loop broke early on len(rows)<requested). Explicit
	// clamp preserves the runaway-query protection without lying about
	// what was returned. See ADR-030 / feedback_no_silent_fallbacks.
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	} else if limit > 100 {
		limit = 100
	}

	where, args := buildInvWhere(filter)
	useCursor := !filter.AfterCreatedAt.IsZero() && filter.AfterID != ""

	if useCursor {
		// Cursor predicate honors the primary sort direction. Only
		// the default `created_at DESC` cursor is supported today —
		// callers asking for a custom Sort+After combination would
		// hit an inconsistent seek-vs-order pairing; the handler
		// rejects those upstream. The (created_at, id) tuple matches
		// invoiceOrderBy's tie-break, so seek + ORDER BY are aligned.
		args = append(args, filter.AfterCreatedAt, filter.AfterID)
		clause := fmt.Sprintf(`(created_at, id) < ($%d, $%d)`, len(args)-1, len(args))
		if where == "" {
			where = " WHERE " + clause
		} else {
			where += " AND " + clause
		}
	}

	// Cursor path skips COUNT — handler derives hasMore from the
	// limit+1 over-fetch. Offset path still needs total for "Page
	// X of N" UX in the legacy dashboard.
	var total int
	if !useCursor {
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM invoices`+where, args...).Scan(&total); err != nil {
			return nil, 0, err
		}
	}

	// Sort with deterministic tie-break on id. Without the tie-break,
	// catchup-generated invoices share microsecond-level created_at
	// values and Postgres returns ties in arbitrary order — looks
	// "random" to operators. Tie-break direction matches the primary
	// sort direction so consecutive ties read as a single ordered
	// group rather than zig-zagging.
	orderBy := invoiceOrderBy(filter.Sort, filter.SortDir)
	queryLimit := limit
	if useCursor {
		queryLimit = limit + 1
	}
	args = append(args, queryLimit)
	query := `SELECT ` + invCols + ` FROM invoices` + where +
		` ORDER BY ` + orderBy +
		` LIMIT $` + fmt.Sprintf("%d", len(args))
	if !useCursor {
		args = append(args, filter.Offset)
		query += ` OFFSET $` + fmt.Sprintf("%d", len(args))
	}

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()

	var invoices []domain.Invoice
	for rows.Next() {
		var inv domain.Invoice
		if err := rows.Scan(s.scanInvDest(&inv)...); err != nil {
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
	var voidedAt, uncollectibleAt *time.Time
	if status == domain.InvoiceVoided {
		voidedAt = &now
	}
	if status == domain.InvoiceUncollectible {
		uncollectibleAt = &now
	}

	var inv domain.Invoice
	err = tx.QueryRowContext(ctx, `
		UPDATE invoices SET status = $1, voided_at = $2,
			uncollectible_at = COALESCE($3, uncollectible_at), updated_at = $4
		WHERE id = $5
		RETURNING `+invCols,
		status, postgres.NullableTime(voidedAt), postgres.NullableTime(uncollectibleAt), now, id,
	).Scan(s.scanInvDest(&inv)...)

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

// FinalizeWithDates flips status to finalized and re-stamps issued_at + due_at
// to the finalize moment in one UPDATE — for operator-composed invoices whose
// draft may have been created on an earlier (test-clock) instant. Cycle
// invoices use UpdateStatus and keep their build-time dates.
func (s *PostgresStore) FinalizeWithDates(ctx context.Context, tenantID, id string, issuedAt, dueAt time.Time) (domain.Invoice, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Invoice{}, err
	}
	defer postgres.Rollback(tx)

	now := clock.Now(ctx)
	var inv domain.Invoice
	err = tx.QueryRowContext(ctx, `
		UPDATE invoices SET status = $1, issued_at = $2, due_at = $3, updated_at = $4
		WHERE id = $5
		RETURNING `+invCols,
		domain.InvoiceFinalized, issuedAt, dueAt, now, id,
	).Scan(s.scanInvDest(&inv)...)

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
	).Scan(s.scanInvDest(&inv)...)

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

// MarkPaymentFailedReportingTransition records a payment failure and reports
// whether THIS call is the first to fire the failure-NOTIFICATION set for this
// PaymentIntent — the payment.failed outbound event, the customer "payment
// failed" email, and auto-started dunning. It is the failed-path analogue of
// MarkPaidReportingTransition, but the dedup key is the PaymentIntent id, not
// the status, because failure is non-terminal: an invoice legitimately re-fails
// once per dunning retry, each with a distinct PI.
//
// SELECT … FOR UPDATE serializes concurrent callers. The inbound webhook dedup
// is a non-atomic read pre-check (payment.HandleWebhook), so two at-least-once
// deliveries of the SAME payment_intent.payment_failed — or a reconciler
// recovery racing the original webhook — can both reach here. The first sets
// failure_notified_pi to this PI and returns firstForThisPI=true; the duplicate
// sees the marker already equals this PI and returns false, so SettleFailed
// fires the notification set once, not twice.
//
// The synchronous charge path stamps payment_status='failed' (same PI) WITHOUT
// firing notifications, deferring them to the webhook — so the marker, which
// that path never writes, is the only reliable discriminator (a status-keyed
// gate would suppress the webhook's notifications entirely).
//
// An already-settled invoice (an out-of-order failure for a charge that already
// succeeded) is left untouched and returns false — the authoritative form of
// SettleFailed's stale-snapshot guard, so a stale failure can never flip a paid
// invoice back to failed.
func (s *PostgresStore) MarkPaymentFailedReportingTransition(ctx context.Context, tenantID, id, paymentIntentID, lastPaymentError string) (domain.Invoice, bool, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Invoice{}, false, err
	}
	defer postgres.Rollback(tx)

	var status, paymentStatus string
	var notifiedPI sql.NullString
	if err := tx.QueryRowContext(ctx,
		`SELECT status, payment_status, failure_notified_pi FROM invoices WHERE id = $1 FOR UPDATE`, id,
	).Scan(&status, &paymentStatus, &notifiedPI); err != nil {
		if err == sql.ErrNoRows {
			return domain.Invoice{}, false, errs.ErrNotFound
		}
		return domain.Invoice{}, false, fmt.Errorf("load invoice for mark-failed: %w", err)
	}

	// Out-of-order failure for an already-settled invoice: never flip paid back
	// to failed (would null paid_at, relink a stale PI, and dun a paid invoice).
	// Return the row unchanged; not a fresh notification.
	if domain.InvoiceStatus(status) == domain.InvoicePaid || domain.InvoicePaymentStatus(paymentStatus) == domain.PaymentSucceeded {
		var inv domain.Invoice
		if err := tx.QueryRowContext(ctx,
			`SELECT `+invCols+` FROM invoices WHERE id = $1`, id,
		).Scan(s.scanInvDest(&inv)...); err != nil {
			return domain.Invoice{}, false, fmt.Errorf("reload settled invoice: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return domain.Invoice{}, false, err
		}
		return inv, false, nil
	}

	firstForThisPI := !notifiedPI.Valid || notifiedPI.String != paymentIntentID

	now := clock.Now(ctx)
	var inv domain.Invoice
	if err := tx.QueryRowContext(ctx, `
		UPDATE invoices SET
			payment_status = 'failed',
			stripe_payment_intent_id = $1,
			last_payment_error = $2,
			failure_notified_pi = $1,
			updated_at = $3
		WHERE id = $4
		RETURNING `+invCols,
		postgres.NullableString(paymentIntentID), postgres.NullableString(lastPaymentError), now, id,
	).Scan(s.scanInvDest(&inv)...); err != nil {
		if err == sql.ErrNoRows {
			return domain.Invoice{}, false, errs.ErrNotFound
		}
		return domain.Invoice{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Invoice{}, false, err
	}
	return inv, firstForThisPI, nil
}

// MarkPaid settles an invoice (status→paid, amount_due→0) and is the
// single transactional outbox point for invoice.paid. Thin wrapper over
// MarkPaidReportingTransition for the many callers that don't care
// whether THIS call did the transition.
func (s *PostgresStore) MarkPaid(ctx context.Context, tenantID, id string, stripePaymentIntentID string, paidAt time.Time) (domain.Invoice, error) {
	inv, _, err := s.MarkPaidReportingTransition(ctx, tenantID, id, stripePaymentIntentID, paidAt)
	return inv, err
}

// MarkPaidReportingTransition is MarkPaid plus a `transitioned` flag:
// true when THIS call moved the invoice finalized/uncollectible → paid,
// false when it was already paid (the idempotent no-op branch). The
// SELECT … FOR UPDATE serializes concurrent callers, so for two
// at-least-once webhook deliveries of the SAME charge exactly one gets
// transitioned=true. SettleSucceeded gates its post-paid side-effects
// (payment.succeeded event, receipt email, card stamp) on the flag so a
// concurrent redelivery — which the stale-read fast-path guard can't
// catch — fires them once, not twice. invoice.paid itself is already
// once-only (enqueued inside this tx, after the no-op branch returns).
func (s *PostgresStore) MarkPaidReportingTransition(ctx context.Context, tenantID, id string, stripePaymentIntentID string, paidAt time.Time) (domain.Invoice, bool, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Invoice{}, false, err
	}
	defer postgres.Rollback(tx)

	// Hard invariant: paying an invoice implies authoritative amounts.
	// Authoritative amounts require status IN (finalized, uncollectible)
	// — draft means tax may still be pending or the operator's still
	// editing line items; voided means the invoice was annulled and
	// must not transition to paid.
	//
	// Also reject when tax_status is still pending/failed: even a
	// finalized invoice with unresolved tax (rare — finalize itself
	// gates on tax_status=ok, but a manual finalize bypassing that
	// gate could create one) would lock the customer at the wrong
	// total. Stripe Tax-equivalent shape.
	//
	// Pre-fix bug (caught 2026-05-22): billOnePeriod's "credits cover
	// 100%, mark paid immediately" branch called MarkPaid on a draft
	// + tax_status=pending invoice, transitioning draft→paid directly
	// and bypassing the finalize gate. Customer charged subtotal-only
	// (tax=0) and tax retry blocked forever (retry requires
	// status='draft', but status is now 'paid').
	//
	// Re-paying an already-paid invoice is allowed (idempotent — the
	// PaymentSucceeded webhook can fire twice on legitimate retries).
	var status, taxStatus string
	if err := tx.QueryRowContext(ctx,
		`SELECT status, tax_status FROM invoices WHERE id = $1 FOR UPDATE`, id,
	).Scan(&status, &taxStatus); err != nil {
		if err == sql.ErrNoRows {
			return domain.Invoice{}, false, errs.ErrNotFound
		}
		return domain.Invoice{}, false, fmt.Errorf("load invoice for mark-paid: %w", err)
	}
	switch domain.InvoiceStatus(status) {
	case domain.InvoiceFinalized, domain.InvoiceUncollectible:
		// ok — transition to paid below.
	case domain.InvoicePaid:
		// Already paid: re-marking is a legitimate idempotent event. The
		// payment_intent.succeeded webhook and the ambiguous-charge reconciler
		// can resolve the SAME charge under different Stripe event ids (the
		// reconciler never goes through event-id dedup), so MarkPaid fires
		// more than once on the unknown-charge recovery path. Return the
		// existing row UNCHANGED rather than re-running the money UPDATE.
		//
		// The money UPDATE is NOT idempotent: `amount_paid_cents =
		// amount_due_cents` reads the row's CURRENT amount_due_cents, which the
		// first MarkPaid already zeroed — so a second call set
		// amount_paid_cents = 0, corrupting the recorded paid amount and
		// blocking card refunds (refunds size against amount_paid_cents).
		// (audit: velox-ops MarkPaid finding)
		var inv domain.Invoice
		if err := tx.QueryRowContext(ctx,
			`SELECT `+invCols+` FROM invoices WHERE id = $1`, id,
		).Scan(s.scanInvDest(&inv)...); err != nil {
			return domain.Invoice{}, false, fmt.Errorf("reload already-paid invoice: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return domain.Invoice{}, false, err
		}
		return inv, false, nil
	default:
		return domain.Invoice{}, false, errs.InvalidState(fmt.Sprintf(
			"cannot mark invoice paid from status %q — finalize the invoice first (tax-pending invoices stay draft until tax resolves; voided invoices are terminal)",
			status,
		))
	}
	if domain.InvoiceTaxStatus(taxStatus) != domain.InvoiceTaxOK {
		// Pending = tax provider hasn't returned a final answer; the
		// invoice total is subtotal-only and would lock if marked paid.
		// Failed = retries exhausted; the operator should void or
		// manually finalize after resolving the tax decision out-of-
		// band, not silently pay at $0 tax.
		// (Tax-exempt customers / regions resolve to tax_status='ok'
		// with tax_amount_cents=0 + tax_exempt_reason — they're not a
		// separate status.)
		return domain.Invoice{}, false, errs.InvalidState(fmt.Sprintf(
			"cannot mark invoice paid with tax_status=%q — wait for tax retry to resolve, then re-attempt",
			taxStatus,
		))
	}

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
	).Scan(s.scanInvDest(&inv)...)

	if err == sql.ErrNoRows {
		return domain.Invoice{}, false, errs.ErrNotFound
	}
	if err != nil {
		return domain.Invoice{}, false, err
	}
	// invoice.paid — enqueued in the SAME tx as the finalized/uncollectible →
	// paid transition. The already-paid branch above returns before reaching
	// here, so this fires EXACTLY ONCE, and it covers every settlement path
	// (card via SettleSucceeded, credits-cover, offline record-payment, dunning
	// recovery, and the reconciler's bare-MarkPaid fallback) without each
	// needing its own dispatch. ADR-040 transactional outbox: if the tx rolls
	// back, no event; if it commits, the dispatcher delivers.
	if s.outbox != nil {
		if _, err := s.outbox.Enqueue(ctx, tx, tenantID, domain.EventInvoicePaid, map[string]any{
			"invoice_id":        inv.ID,
			"invoice_number":    inv.InvoiceNumber,
			"customer_id":       inv.CustomerID,
			"subscription_id":   inv.SubscriptionID,
			"amount_paid_cents": inv.AmountPaidCents,
			"currency":          inv.Currency,
			"paid_at":           paidAt.UTC(),
		}); err != nil {
			return domain.Invoice{}, false, fmt.Errorf("enqueue invoice.paid: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.Invoice{}, false, err
	}
	return inv, true, nil
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
	).Scan(s.scanInvDest(&inv)...)

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
	).Scan(s.scanInvDest(&inv)...)

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
	).Scan(s.scanInvDest(&inv)...)

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
			description, quantity, unit_amount_cents, amount_cents, tax_rate, tax_amount_cents,
			total_amount_cents, currency, pricing_mode, rating_rule_version_id,
			billing_period_start, billing_period_end, metadata, created_at,
			tax_jurisdiction, tax_code, tax_reason, quantity_decimal)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23)
		RETURNING id, invoice_id, tenant_id, line_type, COALESCE(meter_id,''), description,
			quantity, unit_amount_cents, amount_cents, tax_rate, tax_amount_cents,
			total_amount_cents, currency, COALESCE(pricing_mode,''),
			COALESCE(rating_rule_version_id,''), billing_period_start, billing_period_end,
			metadata, created_at, tax_jurisdiction, tax_code, tax_reason, quantity_decimal
	`, id, item.InvoiceID, tenantID, item.LineType, postgres.NullableString(item.MeterID),
		item.Description, item.Quantity, item.UnitAmountCents, item.AmountCents,
		item.TaxRate, item.TaxAmountCents, item.TotalAmountCents, item.Currency,
		postgres.NullableString(item.PricingMode), postgres.NullableString(item.RatingRuleVersionID),
		postgres.NullableTime(item.BillingPeriodStart), postgres.NullableTime(item.BillingPeriodEnd),
		metaJSON, now, item.TaxJurisdiction, item.TaxCode, item.TaxabilityReason, item.QuantityDecimal,
	).Scan(&item.ID, &item.InvoiceID, &item.TenantID, &item.LineType, &item.MeterID,
		&item.Description, &item.Quantity, &item.UnitAmountCents, &item.AmountCents,
		&item.TaxRate, &item.TaxAmountCents, &item.TotalAmountCents, &item.Currency,
		&item.PricingMode, &item.RatingRuleVersionID,
		&item.BillingPeriodStart, &item.BillingPeriodEnd, &metaJSON, &item.CreatedAt,
		&item.TaxJurisdiction, &item.TaxCode, &item.TaxabilityReason, &item.QuantityDecimal)

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
			quantity, unit_amount_cents, amount_cents, tax_rate, tax_amount_cents,
			total_amount_cents, currency, COALESCE(pricing_mode,''),
			COALESCE(rating_rule_version_id,''), billing_period_start, billing_period_end,
			metadata, created_at, tax_jurisdiction, tax_code, tax_reason, quantity_decimal
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
			&item.AmountCents, &item.TaxRate, &item.TaxAmountCents, &item.TotalAmountCents,
			&item.Currency, &item.PricingMode, &item.RatingRuleVersionID,
			&item.BillingPeriodStart, &item.BillingPeriodEnd, &metaJSON, &item.CreatedAt,
			&item.TaxJurisdiction, &item.TaxCode, &item.TaxabilityReason, &item.QuantityDecimal); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(metaJSON, &item.Metadata)
		items = append(items, item)
	}
	return items, rows.Err()
}

// GetOutstandingBalance computes the customer's AR exposure: sum of
// amount_due_cents + count across all finalized invoices in
// payment_status pending/failed/unknown, excluding voided +
// uncollectible. Single tenant-scoped aggregate query; powers the
// "Outstanding balance" card on customer detail.
func (s *PostgresStore) GetOutstandingBalance(ctx context.Context, tenantID, customerID string) (OutstandingBalance, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return OutstandingBalance{}, err
	}
	defer postgres.Rollback(tx)

	var out OutstandingBalance
	err = tx.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(amount_due_cents), 0), COUNT(*)
		FROM invoices
		WHERE customer_id = $1
		  AND payment_status IN ('pending', 'failed', 'unknown')
		  AND status NOT IN ('voided', 'uncollectible', 'draft')
	`, customerID).Scan(&out.TotalCents, &out.UnpaidCount)
	if err != nil {
		return OutstandingBalance{}, err
	}
	return out, nil
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
			description, quantity, unit_amount_cents, amount_cents, tax_rate, tax_amount_cents,
			total_amount_cents, currency, pricing_mode, rating_rule_version_id,
			billing_period_start, billing_period_end, metadata, created_at,
			tax_jurisdiction, tax_code, tax_reason, quantity_decimal)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23)
		RETURNING id, invoice_id, tenant_id, line_type, COALESCE(meter_id,''), description,
			quantity, unit_amount_cents, amount_cents, tax_rate, tax_amount_cents,
			total_amount_cents, currency, COALESCE(pricing_mode,''),
			COALESCE(rating_rule_version_id,''), billing_period_start, billing_period_end,
			metadata, created_at, tax_jurisdiction, tax_code, tax_reason, quantity_decimal
	`, itemID, invoiceID, tenantID, item.LineType, postgres.NullableString(item.MeterID),
		item.Description, item.Quantity, item.UnitAmountCents, item.AmountCents,
		item.TaxRate, item.TaxAmountCents, item.TotalAmountCents, currency,
		postgres.NullableString(item.PricingMode), postgres.NullableString(item.RatingRuleVersionID),
		postgres.NullableTime(item.BillingPeriodStart), postgres.NullableTime(item.BillingPeriodEnd),
		metaJSON, now, item.TaxJurisdiction, item.TaxCode, item.TaxabilityReason, item.QuantityDecimal,
	).Scan(&item.ID, &item.InvoiceID, &item.TenantID, &item.LineType, &item.MeterID,
		&item.Description, &item.Quantity, &item.UnitAmountCents, &item.AmountCents,
		&item.TaxRate, &item.TaxAmountCents, &item.TotalAmountCents, &item.Currency,
		&item.PricingMode, &item.RatingRuleVersionID,
		&item.BillingPeriodStart, &item.BillingPeriodEnd, &metaJSON, &item.CreatedAt,
		&item.TaxJurisdiction, &item.TaxCode, &item.TaxabilityReason, &item.QuantityDecimal)
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
	).Scan(s.scanInvDest(&inv)...)
	if err != nil {
		return domain.InvoiceLineItem{}, domain.Invoice{}, err
	}

	if err := tx.Commit(); err != nil {
		return domain.InvoiceLineItem{}, domain.Invoice{}, err
	}
	return item, inv, nil
}

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
	// Draft-only is the genuine data invariant: tax is mutable while an
	// invoice is a draft and frozen once finalized. The FOR UPDATE lock above
	// re-asserts it under concurrency. The narrower "tax_status in (pending,
	// failed)" restriction is retry-specific POLICY, not an invariant —
	// RetryTaxForInvoice enforces it at the engine layer before reaching here.
	// ComputeTaxForInvoice (finalize-time tax for manual invoices) legitimately
	// runs against a fresh draft whose tax_status is still 'ok', so the store
	// guards only what it must: draft.
	if status != domain.InvoiceDraft {
		return domain.Invoice{}, errs.InvalidState(fmt.Sprintf(
			"invoice tax is only mutable on draft invoices (current: %s)", status))
	}
	_ = taxStatus // selected for the FOR UPDATE lock; no longer gated here

	now := clock.Now(ctx)

	for _, li := range lineItems {
		if li.ID == "" {
			continue
		}
		_, err = tx.ExecContext(ctx, `
			UPDATE invoice_line_items
			SET tax_rate = $3,
			    tax_amount_cents = $4,
			    total_amount_cents = $5,
			    tax_jurisdiction = $6,
			    tax_code = $7,
			    tax_reason = $8
			WHERE invoice_id = $1 AND id = $2
		`, invoiceID, li.ID, li.TaxRate, li.TaxAmountCents, li.TotalAmountCents,
			li.TaxJurisdiction, li.TaxCode, li.TaxabilityReason)
		if err != nil {
			return domain.Invoice{}, fmt.Errorf("update line item tax stamp: %w", err)
		}
	}

	var inv domain.Invoice
	err = tx.QueryRowContext(ctx, `
		UPDATE invoices SET
			tax_amount_cents = $2,
			tax_rate = $3,
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
			subtotal_cents = $18,
			discount_cents = $19,
			amount_due_cents = GREATEST($16 - amount_paid_cents - credits_applied_cents, 0),
			updated_at = $17
		WHERE id = $1
		RETURNING `+invCols,
		invoiceID,
		update.TaxAmountCents, update.TaxRate,
		update.TaxName, update.TaxCountry, update.TaxID,
		// tax_provider / tax_calculation_id are NOT NULL DEFAULT '' (the
		// INSERT path binds them raw). NullableString would map the manual
		// provider's empty calculation id to SQL NULL and trip the NOT NULL
		// constraint, so bind raw here too.
		update.TaxProvider,
		update.TaxCalculationID,
		update.TaxReverseCharge, update.TaxExemptReason,
		string(update.TaxStatus), postgres.NullableTime(update.TaxDeferredAt),
		update.TaxPendingReason, postgres.NullableString(update.TaxErrorCode),
		postgres.NullableTime(update.TaxNextRetryAt),
		update.TotalAmountCents, now,
		// Net subtotal/discount from the tax application — carved out of the
		// gross in tax-inclusive mode, pass-through (== stored header) in
		// exclusive mode. Keeps subtotal − discount + tax == gross.
		update.SubtotalCents, update.DiscountCents,
	).Scan(s.scanInvDest(&inv)...)
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
	out, err := s.createWithLineItemsInTx(ctx, tx, tenantID, inv, items)
	if err != nil {
		return domain.Invoice{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Invoice{}, err
	}
	return out, nil
}

// CreateWithLineItemsTx is the in-transaction variant used by the
// subscription handler's atomic AddItem-with-proration flow: the caller
// has already opened a tx wrapping the sub-item insert, and the
// proration invoice insert needs to share that tx so a failure here
// rolls back the item add too. ADR-030 atomic-proration follow-through.
func (s *PostgresStore) CreateWithLineItemsTx(ctx context.Context, tx *sql.Tx, tenantID string, inv domain.Invoice, items []domain.InvoiceLineItem) (domain.Invoice, error) {
	return s.createWithLineItemsInTx(ctx, tx, tenantID, inv, items)
}

// createWithLineItemsInTx is the shared body. The exported wrappers
// differ only in tx ownership.
func (s *PostgresStore) createWithLineItemsInTx(ctx context.Context, tx *sql.Tx, tenantID string, inv domain.Invoice, items []domain.InvoiceLineItem) (domain.Invoice, error) {
	id := postgres.NewID("vlx_inv")
	// Canonical UPPERCASE ISO-4217 (see Create) — normalize the header and
	// every line so origin (manual vs cycle) can't change the casing and
	// the stored value matches the tenant default the analytics/dunning
	// revenue queries filter on case-sensitively.
	inv.Currency = strings.ToUpper(inv.Currency)
	for i := range items {
		items[i].Currency = strings.ToUpper(items[i].Currency)
	}
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
	// Store the token as an encrypted blob (reversible, for re-send URLs) + a
	// SHA-256 blind index (for lookup). Never store the raw token.
	encToken, tokenHash := s.encodeToken(publicToken)
	err := tx.QueryRowContext(ctx, `
		INSERT INTO invoices (id, tenant_id, customer_id, subscription_id, invoice_number,
			status, payment_status, currency, subtotal_cents, discount_cents, tax_amount_cents,
			tax_rate, tax_name, tax_country, tax_id,
			total_amount_cents, amount_due_cents, amount_paid_cents, credits_applied_cents,
			billing_period_start, billing_period_end, issued_at, due_at,
			net_payment_term_days, memo, footer, metadata, created_at, updated_at,
			source_plan_changed_at, source_subscription_item_id, source_change_type,
			tax_provider, tax_calculation_id, tax_reverse_charge, tax_exempt_reason,
			tax_status, tax_deferred_at, tax_retry_count, tax_pending_reason, tax_error_code, billing_reason,
			stripe_invoice_id, public_token_encrypted, public_token_hash, paid_at, voided_at, is_simulated)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$28,$29,$30,$31,$32,$33,$34,$35,$36,$37,$38,$39,$40,$41,$42,$43,$44,$45,$46,$47)
		RETURNING `+invCols,
		id, tenantID, inv.CustomerID, postgres.NullableString(inv.SubscriptionID), inv.InvoiceNumber,
		inv.Status, inv.PaymentStatus, inv.Currency,
		inv.SubtotalCents, inv.DiscountCents, inv.TaxAmountCents, inv.TaxRate,
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
		postgres.NullableString(encToken), postgres.NullableString(tokenHash),
		postgres.NullableTime(inv.PaidAt), postgres.NullableTime(inv.VoidedAt),
		inv.IsSimulated,
	).Scan(s.scanInvDest(&inv)...)

	if err != nil {
		if mapped := mapInvoiceUniqueViolation(err, inv); mapped != nil {
			return domain.Invoice{}, mapped
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
				description, quantity, unit_amount_cents, amount_cents, tax_rate,
				tax_amount_cents, total_amount_cents, currency, pricing_mode,
				rating_rule_version_id, billing_period_start, billing_period_end, metadata, created_at,
				tax_jurisdiction, tax_code, tax_reason, quantity_decimal)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23)
		`, itemID, inv.ID, tenantID, items[i].LineType, postgres.NullableString(items[i].MeterID),
			items[i].Description, items[i].Quantity, items[i].UnitAmountCents, items[i].AmountCents,
			items[i].TaxRate, items[i].TaxAmountCents, items[i].TotalAmountCents,
			items[i].Currency, postgres.NullableString(items[i].PricingMode),
			postgres.NullableString(items[i].RatingRuleVersionID),
			postgres.NullableTime(items[i].BillingPeriodStart), postgres.NullableTime(items[i].BillingPeriodEnd),
			itemMetaJSON, now, items[i].TaxJurisdiction, items[i].TaxCode, items[i].TaxabilityReason, items[i].QuantityDecimal,
		)
		if err != nil {
			return domain.Invoice{}, fmt.Errorf("create line item %d: %w", i, err)
		}
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
		if err := rows.Scan(s.scanInvDest(&inv)...); err != nil {
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
		if err := rows.Scan(s.scanInvDest(&inv)...); err != nil {
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
	return s.listInflightPayments(ctx, domain.PaymentUnknown, olderThan, limit)
}

// ListProcessingPayments returns invoices stuck in payment_status 'processing'
// older than the cool-off — the dropped-webhook backstop (ADR-049 Phase 2).
// Same tenant-crossing + livemode-scoped shape as ListUnknownPayments.
func (s *PostgresStore) ListProcessingPayments(ctx context.Context, olderThan time.Time, limit int) ([]domain.Invoice, error) {
	return s.listInflightPayments(ctx, domain.PaymentProcessing, olderThan, limit)
}

// listInflightPayments lists invoices in one non-terminal payment_status older
// than the cool-off, for the reconciler sweep. TxBypass crosses tenants for the
// sweep; the livemode filter prevents test-mode rows from being reconciled
// under a live ctx (see #13). Status is a trusted internal enum (not user
// input), interpolated into the predicate.
func (s *PostgresStore) listInflightPayments(ctx context.Context, status domain.InvoicePaymentStatus, olderThan time.Time, limit int) ([]domain.Invoice, error) {
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
		WHERE payment_status = $1
		  AND updated_at < $2
		  AND livemode = $3
		ORDER BY updated_at ASC
		LIMIT $4
	`, string(status), olderThan, postgres.Livemode(ctx), limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var invoices []domain.Invoice
	for rows.Next() {
		var inv domain.Invoice
		if err := rows.Scan(s.scanInvDest(&inv)...); err != nil {
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
		if err := rows.Scan(s.scanInvDest(&inv)...); err != nil {
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
		if err := rows.Scan(s.scanInvDest(&inv)...); err != nil {
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
		if err := rows.Scan(s.scanInvDest(&inv)...); err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

func (s *PostgresStore) scanInvDest(inv *domain.Invoice) []any {
	var metaJSON []byte
	return []any{
		&inv.ID, &inv.TenantID, &inv.CustomerID, &inv.SubscriptionID, &inv.InvoiceNumber,
		&inv.Status, &inv.PaymentStatus, &inv.Currency,
		&inv.SubtotalCents, &inv.DiscountCents, &inv.TaxAmountCents, &inv.TaxRate,
		&inv.TaxName, &inv.TaxCountry, &inv.TaxID,
		&inv.TotalAmountCents, &inv.AmountDueCents, &inv.AmountPaidCents, &inv.CreditsAppliedCents,
		&inv.BillingPeriodStart, &inv.BillingPeriodEnd,
		&inv.IssuedAt, &inv.DueAt, &inv.PaidAt, &inv.VoidedAt, &inv.UncollectibleAt,
		&inv.StripePaymentIntentID, &inv.LastPaymentError,
		&inv.PaymentOverdue, &inv.AutoChargePending, &inv.NetPaymentTermDays, &inv.Memo, &inv.Footer,
		&metaJSON, &inv.CreatedAt, &inv.UpdatedAt, &inv.SourcePlanChangedAt,
		&inv.SourceSubscriptionItemID, (*string)(&inv.SourceChangeType),
		&inv.TaxProvider, &inv.TaxCalculationID, &inv.TaxTransactionID,
		&inv.TaxReverseCharge, &inv.TaxExemptReason,
		(*string)(&inv.TaxStatus), &inv.TaxDeferredAt, &inv.TaxRetryCount, &inv.TaxPendingReason,
		&inv.TaxErrorCode, &inv.TaxNextRetryAt,
		&inv.PaymentCardBrand, &inv.PaymentCardLast4,
		decryptScanner{enc: s.enc, dst: &inv.PublicToken}, (*string)(&inv.BillingReason), &inv.StripeInvoiceID,
		&inv.IsSimulated,
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

	encToken, tokenHash := s.encodeToken(token)
	// Public-token rotation is an operational metadata write (re-issue a
	// hosted-invoice URL), not a billing-domain transition — stamp updated_at
	// wall-clock via SQL now(), matching SetPaymentCard. Domain transitions
	// (status, tax, paid_at) are the ones that ride the test clock.
	res, err := tx.ExecContext(ctx, `
		UPDATE invoices SET public_token_encrypted = $1, public_token_hash = $2, updated_at = now()
		WHERE id = $3 AND status <> 'draft'
	`, postgres.NullableString(encToken), postgres.NullableString(tokenHash), invoiceID)
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
	dest := append(s.scanInvDest(&inv), &livemode)
	err = tx.QueryRowContext(ctx, `SELECT `+invCols+`, livemode FROM invoices WHERE public_token_hash = $1`, HashPublicToken(token)).
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
		Scan(s.scanInvDest(&inv)...)
	if err == sql.ErrNoRows {
		return domain.Invoice{}, errs.ErrNotFound
	}
	return inv, err
}

// FindBaseInvoiceForPeriod is the proration-credit gate: returns the
// invoice carrying an in_advance base line for the given subscription
// period. The line's `billing_period_start` matches the period the
// base fee covers (industry-standard semantic — Stripe / Lago / Orb /
// Chargebee align). Caller reads invoice.PaymentStatus to decide
// whether a "refundable" credit (Chargebee Refundable) or "skip /
// adjust unpaid invoice" (Chargebee Adjustment / Stripe none) shape
// applies.
//
// Voided / uncollectible invoices are filtered out — they don't
// represent revenue the customer paid for, even if their line items
// match the period. ORDER BY created_at DESC + LIMIT 1 surfaces the
// most-recent matching invoice (relevant for plan-swap chains within
// the same period that re-issued the base line).
func (s *PostgresStore) FindBaseInvoiceForPeriod(ctx context.Context, tenantID, subscriptionID string, periodStart time.Time) (domain.Invoice, error) {
	if subscriptionID == "" {
		return domain.Invoice{}, errs.ErrNotFound
	}
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Invoice{}, err
	}
	defer postgres.Rollback(tx)

	var inv domain.Invoice
	err = tx.QueryRowContext(ctx, `
		SELECT `+invCols+` FROM invoices i
		WHERE i.subscription_id = $1
		  AND i.status NOT IN ('voided', 'uncollectible')
		  AND EXISTS (
		    SELECT 1 FROM invoice_line_items li
		    WHERE li.invoice_id = i.id
		      AND li.line_type = 'base_fee'
		      AND li.billing_period_start = $2
		  )
		ORDER BY i.created_at DESC
		LIMIT 1
	`, subscriptionID, periodStart).Scan(s.scanInvDest(&inv)...)
	if err == sql.ErrNoRows {
		return domain.Invoice{}, errs.ErrNotFound
	}
	return inv, err
}

// LatestThresholdPeriodEnd returns the latest billing_period_end across
// the subscription's threshold-fired invoices for the cycle starting at
// periodStart. The billing engine's cycle close uses it as the
// "already billed through" watermark: usage before that instant (and
// the in_arrears base fee) landed on the threshold invoice, so the
// cycle-close invoice must bill only the residual window — otherwise a
// reset_billing_cycle=false threshold double-bills the customer.
//
// Window-scoped by billing_period_start ∈ [periodStart, periodEnd) so
// the watermark generalizes to multiple threshold fires per cycle
// (each successive fire's period starts at the previous one's end).
// Voided / uncollectible invoices don't count — their amounts were
// returned or written off, so the usage they covered is billable again.
// Returns errs.ErrNotFound when no threshold invoice fired this cycle.
func (s *PostgresStore) LatestThresholdPeriodEnd(ctx context.Context, tenantID, subscriptionID string, periodStart, periodEnd time.Time) (time.Time, error) {
	if subscriptionID == "" {
		return time.Time{}, errs.ErrNotFound
	}
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return time.Time{}, err
	}
	defer postgres.Rollback(tx)

	var end sql.NullTime
	err = tx.QueryRowContext(ctx, `
		SELECT MAX(billing_period_end) FROM invoices
		WHERE subscription_id = $1
		  AND billing_reason = 'threshold'
		  AND status NOT IN ('voided', 'uncollectible')
		  AND billing_period_start >= $2
		  AND billing_period_start < $3
	`, subscriptionID, periodStart, periodEnd).Scan(&end)
	if err != nil {
		return time.Time{}, err
	}
	if !end.Valid {
		return time.Time{}, errs.ErrNotFound
	}
	return end.Time, nil
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
		idx++
	}
	if f.Search != "" {
		clauses = append(clauses, fmt.Sprintf("invoice_number ILIKE $%d", idx))
		args = append(args, "%"+postgres.EscapeLike(f.Search)+"%")
		idx++
	}
	if !f.CreatedFrom.IsZero() {
		clauses = append(clauses, fmt.Sprintf("created_at >= $%d", idx))
		args = append(args, f.CreatedFrom)
		idx++
	}
	if !f.CreatedTo.IsZero() {
		clauses = append(clauses, fmt.Sprintf("created_at <= $%d", idx))
		args = append(args, f.CreatedTo)
		idx++
	}
	if f.Overdue {
		// "Past due" = open, past its due date, not settled, not
		// mid-payment. Mirrors domain.ClassifyInvoiceAttention's
		// overdue semantics, but computed in SQL so the filter
		// paginates server-side. due_at compares against DB
		// wall-clock now(); test-clock invoices stamp due_at in
		// simulated time, so a frozen-future clock keeps its
		// invoices out of this view until wall-clock catches up —
		// the same trade Stripe's dashboard makes, and the per-row
		// attention dot still reflects the authoritative state.
		clauses = append(clauses, fmt.Sprintf(
			"(status = $%d AND due_at IS NOT NULL AND due_at < now() AND payment_status NOT IN ($%d, $%d))",
			idx, idx+1, idx+2))
		args = append(args,
			string(domain.InvoiceFinalized),
			string(domain.PaymentSucceeded),
			string(domain.PaymentProcessing))
		idx += 3
	}
	if len(f.IDs) > 0 {
		// ids=... filter — same shape as customer.ListFilter.IDs.
		// Lets CreditNotes-and-similar pages fetch the exact invoices
		// their primary rows reference without depending on the
		// default-pagination of an unrelated invoice list.
		placeholders := make([]string, len(f.IDs))
		for i, id := range f.IDs {
			placeholders[i] = fmt.Sprintf("$%d", idx)
			args = append(args, id)
			idx++
		}
		clauses = append(clauses, "id IN ("+strings.Join(placeholders, ",")+")")
	}

	if len(clauses) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}
