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

// cnReadCols is the canonical credit-note SELECT/RETURNING column list.
// Every read site uses it with cnScanDest so a schema change (e.g. adding
// is_simulated) updates one place and can't drift a positional scan.
const cnReadCols = `id, tenant_id, invoice_id, customer_id, credit_note_number,
	status, reason, subtotal_cents, tax_amount_cents, total_cents,
	refund_amount_cents, credit_amount_cents, out_of_band_amount_cents,
	currency, issued_at, voided_at, refund_status, COALESCE(stripe_refund_id,''),
	COALESCE(tax_transaction_id,''), is_simulated, issue_pending,
	tax_reversal_pending,
	metadata, created_at, updated_at`

// cnScanDest returns Scan destinations in cnReadCols order. metaJSON is the
// raw metadata bytes the caller unmarshals into cn.Metadata after Scan.
func cnScanDest(cn *domain.CreditNote, metaJSON *[]byte) []any {
	return []any{
		&cn.ID, &cn.TenantID, &cn.InvoiceID, &cn.CustomerID, &cn.CreditNoteNumber,
		&cn.Status, &cn.Reason, &cn.SubtotalCents, &cn.TaxAmountCents, &cn.TotalCents,
		&cn.RefundAmountCents, &cn.CreditAmountCents, &cn.OutOfBandAmountCents,
		&cn.Currency, &cn.IssuedAt, &cn.VoidedAt, &cn.RefundStatus, &cn.StripeRefundID,
		&cn.TaxTransactionID, &cn.IsSimulated, &cn.IssuePending,
		&cn.TaxReversalPending,
		metaJSON, &cn.CreatedAt, &cn.UpdatedAt,
	}
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
// credit note to insert), and inserts the note PLUS its line items — all in
// the one transaction. A concurrent Create for the same invoice blocks on the
// lock until this transaction commits, then sees this credit note in its own
// list, so the caps can't be bypassed by a time-of-check/time-of-use race.
// The lock releases automatically on commit.
//
// Lines commit with the header or not at all. Pre-fix the header committed
// here and each line was inserted afterwards in its own transaction — a crash
// or failure between them left an orphan credit note with a non-zero total
// and zero lines, which Issue() would still act on.
func (s *PostgresStore) CreateUnderInvoiceLock(ctx context.Context, tenantID, invoiceID string, lines []domain.CreditNoteLineItem, build func(existing []domain.CreditNote) (domain.CreditNote, error)) (domain.CreditNote, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.CreditNote{}, err
	}
	defer postgres.Rollback(tx)

	created, err := s.createUnderInvoiceLockInTx(ctx, tx, tenantID, invoiceID, lines, build)
	if err != nil {
		return domain.CreditNote{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.CreditNote{}, err
	}
	return created, nil
}

// CreateUnderInvoiceLockTx is CreateUnderInvoiceLock on the CALLER's transaction
// (coordinator-owned *sql.Tx, ADR-056) — so the credit note + its lines commit
// ATOMICALLY with the caller's other writes (e.g. a subscription item delete in
// atomicRemoveItemWithProration). A failure on either side rolls back both: the
// item change AND the clawback obligation, closing the post-commit
// fire-and-forget gap where a removed item could leave the customer
// un-credited. The per-invoice advisory lock is taken on the caller's tx and
// releases when it commits. The caller owns Begin/Commit/Rollback.
func (s *PostgresStore) CreateUnderInvoiceLockTx(ctx context.Context, tx *sql.Tx, tenantID, invoiceID string, lines []domain.CreditNoteLineItem, build func(existing []domain.CreditNote) (domain.CreditNote, error)) (domain.CreditNote, error) {
	return s.createUnderInvoiceLockInTx(ctx, tx, tenantID, invoiceID, lines, build)
}

func (s *PostgresStore) createUnderInvoiceLockInTx(ctx context.Context, tx *sql.Tx, tenantID, invoiceID string, lines []domain.CreditNoteLineItem, build func(existing []domain.CreditNote) (domain.CreditNote, error)) (domain.CreditNote, error) {
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
	for i, line := range lines {
		line.CreditNoteID = created.ID
		if _, err := s.insertLineItemTx(ctx, tx, tenantID, line); err != nil {
			return domain.CreditNote{}, fmt.Errorf("create line item %d: %w", i+1, err)
		}
	}
	return created, nil
}

// ListPendingClawbackDrafts returns auto-issue clawback drafts (issue_pending,
// still status='draft') that are READY to issue, for the RetryPendingClawbackIssue
// reconciler. Cross-tenant (TxBypass) + scoped by livemode, mirroring
// ListPendingTaxCommit; each row carries its TenantID so the service re-issues
// under the right tenant.
//
// "Ready" = the source invoice's payment is NOT in flight (processing/unknown).
// This is the defer-until-settle gate (ADR-059): an automated clawback against a
// source whose charge is still settling is created as a draft but NOT issued —
// issuing it now would reduce amount_due before MarkPaid records the captured
// amount (under-record) or relieve a charge that may yet succeed. The draft waits
// here until the source reaches a terminal payment state, at which point Issue()'s
// payment_status branch picks the right channel (paid→credit, failed→reduce).
//
// There is deliberately NO time window: an off-session SCA source can sit
// 'processing' for days, so a fixed horizon (the prior 24h bound) would age a
// legitimately-deferred draft out of the scan and silently drop the customer's
// relief. The NOT-EXISTS source-terminal gate is the only eligibility predicate;
// once a draft's source settles it becomes eligible regardless of draft age.
// RetryPendingClawbackIssue logs per-row re-issue failures, so a genuinely-stuck
// draft is surfaced by repeated ERROR logs rather than silently aged out.
func (s *PostgresStore) ListPendingClawbackDrafts(ctx context.Context, batch int, livemode bool) ([]domain.CreditNote, error) {
	if batch <= 0 {
		batch = 50
	}
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	rows, err := tx.QueryContext(ctx, `SELECT `+cnReadCols+`
		FROM credit_notes
		WHERE livemode = $1
		  AND issue_pending
		  AND status = 'draft'
		  AND NOT EXISTS (
		    SELECT 1 FROM invoices i
		    WHERE i.id = credit_notes.invoice_id
		      AND i.tenant_id = credit_notes.tenant_id
		      AND i.payment_status IN ('processing', 'unknown')
		  )
		ORDER BY updated_at ASC
		LIMIT $2`, livemode, batch)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var notes []domain.CreditNote
	for rows.Next() {
		var cn domain.CreditNote
		var metaJSON []byte
		if err := rows.Scan(cnScanDest(&cn, &metaJSON)...); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(metaJSON, &cn.Metadata)
		notes = append(notes, cn)
	}
	return notes, rows.Err()
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
			currency, refund_status, is_simulated, issue_pending, metadata, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$19)
		RETURNING `+cnReadCols,
		id, tenantID, cn.InvoiceID, cn.CustomerID, cn.CreditNoteNumber,
		cn.Status, cn.Reason, cn.SubtotalCents, cn.TaxAmountCents, cn.TotalCents,
		cn.RefundAmountCents, cn.CreditAmountCents, cn.OutOfBandAmountCents,
		cn.Currency, cn.RefundStatus, cn.IsSimulated, cn.IssuePending, metaJSON, now,
	).Scan(cnScanDest(&cn, &metaJSON)...)
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
	rows, err := tx.QueryContext(ctx, `SELECT `+cnReadCols+`
		FROM credit_notes WHERE invoice_id = $1`, invoiceID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var notes []domain.CreditNote
	for rows.Next() {
		var cn domain.CreditNote
		var metaJSON []byte
		if err := rows.Scan(cnScanDest(&cn, &metaJSON)...); err != nil {
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
	err = tx.QueryRowContext(ctx, `SELECT `+cnReadCols+`
		FROM credit_notes WHERE id = $1
	`, id).Scan(cnScanDest(&cn, &metaJSON)...)
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

	query := `SELECT ` + cnReadCols + ` FROM credit_notes`
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
	if filter.RefundStatus == "needs_attention" {
		// failed OR pending — the operator-actionable refund states. Literal,
		// no arg (closed set), so no injection surface.
		clauses = append(clauses, "refund_status IN ('failed', 'pending')")
	} else if filter.RefundStatus != "" {
		clauses = append(clauses, fmt.Sprintf("refund_status = $%d", idx))
		args = append(args, filter.RefundStatus)
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
		if err := rows.Scan(cnScanDest(&cn, &metaJSON)...); err != nil {
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
// BeginTx opens an RLS-scoped (TxTenant) coordinator transaction the
// creditnote Service owns for Issue(): the draft→issued CAS and the internal
// money effect (amount_due reduction or credit grant) run on this one tx and
// commit/roll back together (ADR-056 / ADR-061). The caller owns
// Commit/Rollback. Cross-domain stores (invoice, credit) run their *Tx methods
// on this same handle — one Postgres, one transaction, one RLS context.
func (s *PostgresStore) BeginTx(ctx context.Context, tenantID string) (*sql.Tx, error) {
	return s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
}

func (s *PostgresStore) TransitionStatus(ctx context.Context, tenantID, id string, from, to domain.CreditNoteStatus) (bool, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return false, err
	}
	defer postgres.Rollback(tx)

	won, err := s.TransitionStatusTx(ctx, tx, tenantID, id, from, to)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return won, nil
}

// TransitionStatusTx is TransitionStatus on the caller's coordinator tx: the
// compare-and-swap runs but is NOT committed here, so Issue() can fold the
// status flip and the internal money effect into one atomic commit. The CAS
// (UPDATE ... WHERE status=$from) still serializes concurrent/retried Issue()
// calls — exactly one caller gets won=true.
func (s *PostgresStore) TransitionStatusTx(ctx context.Context, tx *sql.Tx, tenantID, id string, from, to domain.CreditNoteStatus) (bool, error) {
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

	if err := s.UpdateAllocationTx(ctx, tx, tenantID, id, refundCents, creditCents, outOfBandCents); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateAllocationTx is UpdateAllocation on the caller's coordinator tx, so the
// re-derived allocation commits atomically with the draft→issued CAS and the
// credit grant during Issue().
func (s *PostgresStore) UpdateAllocationTx(ctx context.Context, tx *sql.Tx, tenantID, id string, refundCents, creditCents, outOfBandCents int64) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE credit_notes SET refund_amount_cents=$1, credit_amount_cents=$2,
			out_of_band_amount_cents=$3, updated_at=$4
		WHERE id=$5`,
		refundCents, creditCents, outOfBandCents, clock.Now(ctx), id)
	return err
}

// SetTaxReversalPending flips the recovery marker for a credit note's
// POST-COMMIT upstream tax reversal: set true when the reversal is attempted and
// fails (→ RetryPendingCreditNoteTaxReversal re-drives it with the per-CN key),
// cleared on success. Runs in its own tx — the reversal is a post-commit
// external effect, not part of the issue coordinator tx.
func (s *PostgresStore) SetTaxReversalPending(ctx context.Context, tenantID, id string, pending bool) error {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return err
	}
	defer postgres.Rollback(tx)

	_, err = tx.ExecContext(ctx, `
		UPDATE credit_notes SET tax_reversal_pending=$1, updated_at=$2 WHERE id=$3`,
		pending, clock.Now(ctx), id)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// ListPendingCreditNoteTaxReversal returns issued credit notes whose POST-COMMIT
// upstream tax reversal still needs to run, cross-tenant + scoped by livemode,
// for RetryPendingCreditNoteTaxReversal. The CN-path counterpart to invoice #310
// RetryPendingTaxReversal, which scans only voided/uncollectible invoices and so
// is structurally blind to a CN reversal stamped on a finalized/paid invoice.
//
// Eligibility is DERIVABLE FROM DURABLE STATE, not dependent on a marker write
// landing (the #310 design): a row qualifies if EITHER
//
//	(a) tax_reversal_pending — the fast path: the inline attempt failed and set
//	    the marker (cheap, partial-index-backed); recovered until success, or
//	(b) the structural over-remit state — an issued CN with NO reversal stamped
//	    (tax_transaction_id='') against a tax-bearing stripe_tax source invoice.
//
// (b) is the backstop for the compound failure where BOTH ReverseTax AND the
// marker write fail in the same Issue(): the orphan (issued, no tx id, marker
// false) would otherwise be invisible to every sweep — a permanent silent
// over-remit. (b) is bounded to a 24h freshness window (anti-churn + no
// first-deploy re-reversal burst over pre-feature CNs, matching #310); a
// transient failure resolves in seconds-to-minutes, well inside it. The marker
// branch is unbounded (a known owed reversal is never aged out). Re-reversal is
// Stripe-idempotent via the per-CN velox_tax_rev_<cn.ID> reference, so any
// overlap between (a) and (b), or with the inline attempt, dedups to one.
func (s *PostgresStore) ListPendingCreditNoteTaxReversal(ctx context.Context, batch int, livemode bool) ([]domain.CreditNote, error) {
	if batch <= 0 {
		batch = 50
	}
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	rows, err := tx.QueryContext(ctx, `SELECT `+cnReadCols+`
		FROM credit_notes
		WHERE livemode = $1
		  AND status = 'issued'
		  AND is_simulated = false
		  AND (
		        tax_reversal_pending
		     OR (
		            COALESCE(tax_transaction_id, '') = ''
		        AND total_cents > 0
		        AND updated_at > now() - interval '24 hours'
		        AND EXISTS (
		              SELECT 1 FROM invoices i
		              WHERE i.id = credit_notes.invoice_id
		                AND i.tenant_id = credit_notes.tenant_id
		                AND i.tax_provider = 'stripe_tax'
		                AND COALESCE(i.tax_transaction_id, '') <> ''
		            )
		        )
		  )
		ORDER BY updated_at ASC
		LIMIT $2`, livemode, batch)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var notes []domain.CreditNote
	for rows.Next() {
		var cn domain.CreditNote
		var metaJSON []byte
		if err := rows.Scan(cnScanDest(&cn, &metaJSON)...); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(metaJSON, &cn.Metadata)
		notes = append(notes, cn)
	}
	return notes, rows.Err()
}

// insertLineItemTx writes one line item inside the caller's transaction —
// only ever called from CreateUnderInvoiceLock so a line can't exist without
// its committed header (and vice versa).
func (s *PostgresStore) insertLineItemTx(ctx context.Context, tx *sql.Tx, tenantID string, item domain.CreditNoteLineItem) (domain.CreditNoteLineItem, error) {
	id := postgres.NewID("vlx_cnli")
	now := clock.Now(ctx)
	_, err := tx.ExecContext(ctx, `
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
