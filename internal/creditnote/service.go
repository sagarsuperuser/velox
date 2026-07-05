package creditnote

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
	"github.com/sagarsuperuser/velox/internal/tax"
)

// InvoiceReader reads invoice data for credit note validation.
type InvoiceReader interface {
	Get(ctx context.Context, tenantID, id string) (domain.Invoice, error)
	ListLineItems(ctx context.Context, tenantID, invoiceID string) ([]domain.InvoiceLineItem, error)
	ApplyCreditNote(ctx context.Context, tenantID, id string, amountCents int64) (domain.Invoice, error)
	// ApplyCreditNoteTx reduces amount_due on the caller's coordinator tx, so
	// the reduction commits atomically with Issue()'s draft→issued CAS (ADR-061).
	ApplyCreditNoteTx(ctx context.Context, tx *sql.Tx, tenantID, id string, amountCents int64) (domain.Invoice, error)
}

// Refunder processes refunds via the payment provider.
//
// `idempotencyKey` is passed to the provider (Stripe-style) so a retry
// after a partial failure in Issue() returns the existing refund_id
// rather than creating a duplicate refund. Callers MUST pass a
// deterministic key tied to the credit-note id (Velox uses
// `velox_cn_<cn_id>`).
type Refunder interface {
	// Returns the refund id + Stripe's create-time status (mapped to a Velox
	// refund_status). A pending result settles later via a refund webhook.
	CreateRefund(ctx context.Context, paymentIntentID string, amountCents int64, idempotencyKey string) (string, domain.RefundStatus, error)
}

// CreditGranter adds credits to a customer's balance.
//
// `GrantForCreditNote` is the retry-safe variant used by Issue(): the
// implementation MUST dedup on (tenant, creditNoteID) so a retry after
// partial-failure of Issue() doesn't append a duplicate grant.
// Migration 0093 backs this via a partial unique index. On dedup hit
// the existing grant is returned silently.
type CreditGranter interface {
	Grant(ctx context.Context, tenantID string, input CreditGrantInput) error
	GrantForCreditNote(ctx context.Context, tenantID, creditNoteID string, input CreditGrantInput) error
	// GrantForCreditNoteTx grants on the caller's coordinator tx, so the credit
	// ledger entry commits atomically with Issue()'s draft→issued CAS (ADR-061).
	// The 0093 dedup index still backs it, but the CAS makes it unreachable: a
	// dedup conflict here would mean a second CAS winner, which cannot happen.
	GrantForCreditNoteTx(ctx context.Context, tx *sql.Tx, tenantID, creditNoteID string, input CreditGrantInput) error
}

// TaxReverser issues a reversal of the invoice's committed tax transaction.
// Called from Issue when the invoice has a non-empty TaxTransactionID. The
// implementation (billing.Engine.ReverseTax) resolves the tenant's tax
// provider and forwards the ReversalRequest. Optional — when nil, Issue
// proceeds without reversing upstream tax (suitable for tenants on
// none/manual providers, or for tests).
type TaxReverser interface {
	ReverseTax(ctx context.Context, tenantID string, req tax.ReversalRequest) (*tax.ReversalResult, error)
}

// NumberGenerator generates sequential credit note numbers.
type NumberGenerator interface {
	NextCreditNoteNumber(ctx context.Context, tenantID string) (string, error)
}

type CreditGrantInput struct {
	CustomerID  string
	AmountCents int64
	Description string
	InvoiceID   string
}

type Service struct {
	store    Store
	invoices InvoiceReader
	refunder Refunder
	credits  CreditGranter
	numbers  NumberGenerator
	taxRev   TaxReverser
}

func NewService(store Store, invoices InvoiceReader, refunder Refunder, credits ...CreditGranter) *Service {
	s := &Service{store: store, invoices: invoices, refunder: refunder}
	if len(credits) > 0 {
		s.credits = credits[0]
	}
	return s
}

// SetNumberGenerator sets the sequential number generator (breaks circular dep).
func (s *Service) SetNumberGenerator(ng NumberGenerator) {
	s.numbers = ng
}

// SetTaxReverser wires the billing engine (or test stub) that issues
// upstream tax reversals when a credit note is issued. Optional — when
// unset, Issue skips the reversal step and logs.
func (s *Service) SetTaxReverser(tr TaxReverser) {
	s.taxRev = tr
}

// CreateInput is the public payload for POST /v1/credit_notes. Three
// allocation fields mirror Stripe (`refund_amount` / `credit_amount` /
// `out_of_band_amount`) and Lago (`refund_amount_cents` /
// `credit_amount_cents` / `offset_amount_cents`): the operator splits
// the total across (a) Stripe refund to the payment method,
// (b) restore-to-customer-credit-balance, and (c) handled outside
// Stripe (cash, ACH, manual adjustment).
//
// For paid invoices, the three amounts must sum to the CN total. For
// unpaid invoices the allocation is ignored (the CN reduces amount_due
// directly). If the caller leaves all three fields zero on a paid
// invoice, the default is `credit_amount = total` — the safest fallback
// (no cash movement, restorable later). Legacy callers can pass
// RefundType="refund"/"credit" and the field is translated to the
// equivalent single-channel split.
type CreateInput struct {
	InvoiceID            string            `json:"invoice_id"`
	Reason               string            `json:"reason"`
	Lines                []CreditLineInput `json:"lines"`
	RefundAmountCents    int64             `json:"refund_amount_cents"`
	CreditAmountCents    int64             `json:"credit_amount_cents"`
	OutOfBandAmountCents int64             `json:"out_of_band_amount_cents"`
	// RefundType is the legacy single-channel selector ("refund" |
	// "credit"). Kept for back-compat — callers that set it have the
	// matching allocation field populated automatically when the new
	// fields are all zero. New integrations should use the three
	// explicit *_cents fields directly.
	RefundType string `json:"refund_type,omitempty"`
	AutoIssue  bool   `json:"auto_issue"` // create + issue atomically
	// IsSimulated marks this issuance as running in the invoice's
	// (possibly simulated) time domain — set true by the engine clawback
	// path, which issues under the clock-pinned sub's bound effective-now.
	// The operator HTTP path leaves it false (it never binds a clock, so
	// issued_at is wall-clock). buildCreditNote ANDs it with the invoice's
	// own is_simulated, so an engine CN on a non-simulated invoice still
	// resolves to false. NOT a JSON/API field — callers set it in Go.
	IsSimulated bool `json:"-"`
	// IssuePending marks an AUTO-ISSUE clawback draft (migration 0121): created
	// in-tx with a subscription downgrade/removal/qty-decrease via a tx-accepting
	// create, then issued post-commit. If Issue() never runs (crash in the
	// post-commit window, status still 'draft') RetryPendingClawbackIssue
	// re-issues it; a post-flip partial failure is not auto-recovered (see that
	// method). Set only by CreateAdjustmentDraftTx; operator HTTP creates leave
	// it false. NOT a JSON/API field — callers set it in Go.
	IssuePending bool `json:"-"`
}

type CreditLineInput struct {
	InvoiceLineItemID string `json:"invoice_line_item_id,omitempty"`
	Description       string `json:"description"`
	Quantity          int64  `json:"quantity"`
	UnitAmountCents   int64  `json:"unit_amount_cents"`
}

func (s *Service) Create(ctx context.Context, tenantID string, input CreateInput) (domain.CreditNote, error) {
	// In-flight payment gate. An OPERATOR credit note must not reduce an
	// invoice's amount_due while its payment is in flight (processing/unknown):
	// at settle, MarkPaid records amount_paid off the now-lower amount_due
	// (invoice/postgres.go KNOWN EDGE), under-sizing the refund cap. So block it
	// — the operator settles or cancels the payment first. Consistent with
	// Stripe's open-payment lifecycle posture (it blocks void/edit/uncollectible
	// while a payment is open) and with our own RecordPayment guard
	// (invoice/service.go: "a charge is already in flight … wait or cancel").
	//
	// payment_status is the sufficient signal: a PAID invoice reads 'succeeded'
	// and takes the refund/credit-balance branch (never reduces amount_due), so
	// it is never gated. The AUTOMATED clawback paths (ADR-050 unpaid-source:
	// CreateAndIssueAdjustment / CreateAdjustmentDraftTx) call create() DIRECTLY,
	// bypassing this 409 gate — they cannot bounce a cancel/downgrade back to a
	// human. Instead they DEFER: Issue() leaves the draft unissued while the
	// source is in-flight and the reconciler issues it once the source settles
	// (ADR-059). A Get error here falls through to create(), which raises the
	// canonical not-found/validation error.
	if input.InvoiceID != "" {
		if inv, err := s.invoices.Get(ctx, tenantID, input.InvoiceID); err == nil &&
			inv.PaymentStatus.IsInFlight() {
			return domain.CreditNote{}, errs.InvalidState(
				"cannot credit-note an invoice whose payment is in flight — settle or cancel the payment first, or wait for charge reconciliation")
		}
	}
	return s.create(ctx, tenantID, input, nil)
}

// create is Create's tx-aware core. tx==nil uses the store's own transaction
// (the operator HTTP path). A non-nil tx threads the credit-note insert into
// the CALLER's transaction (coordinator-owned *sql.Tx, ADR-056) so the note
// commits ATOMICALLY with the caller's other writes — used by
// CreateAdjustmentDraftTx so a subscription item delete and its clawback
// obligation roll back together.
func (s *Service) create(ctx context.Context, tenantID string, input CreateInput, tx *sql.Tx) (domain.CreditNote, error) {
	if input.InvoiceID == "" {
		return domain.CreditNote{}, errs.Required("invoice_id")
	}
	if strings.TrimSpace(input.Reason) == "" {
		return domain.CreditNote{}, errs.Required("reason")
	}
	if len(input.Lines) == 0 {
		return domain.CreditNote{}, errs.Invalid("lines", "at least one line item is required")
	}

	for i, line := range input.Lines {
		if line.Quantity <= 0 {
			return domain.CreditNote{}, errs.Invalid("lines", fmt.Sprintf("line %d: quantity must be positive", i+1))
		}
		if line.UnitAmountCents <= 0 {
			return domain.CreditNote{}, errs.Invalid("lines", fmt.Sprintf("line %d: amount must be greater than 0", i+1))
		}
	}

	// Verify invoice exists and is finalized or paid
	inv, err := s.invoices.Get(ctx, tenantID, input.InvoiceID)
	if err != nil {
		return domain.CreditNote{}, fmt.Errorf("invoice not found: %w", err)
	}
	if inv.Status == domain.InvoiceVoided {
		return domain.CreditNote{}, errs.InvalidState("cannot create credit notes for voided invoices")
	}
	if inv.Status != domain.InvoiceFinalized && inv.Status != domain.InvoicePaid {
		return domain.CreditNote{}, errs.InvalidState("can only create credit notes for finalized or paid invoices")
	}
	// ADR-078 D4: commit funding invoices are cash instruments — no credit
	// notes, phase 1. Pre-payment, a concession CN would shrink the cash
	// collected while the grant stays configured full-size; post-payment, a
	// refund CN returns cash while the funded block stays drawable (and a
	// credit-settled CN would ADD a second grant on top). Unwind paths:
	// unpaid → void the invoice (retires the grant, ADR-078 D3); paid →
	// blocked until the CN-retire leg ships (phase 2 — trigger: first DP
	// commit-refund ask). Checked in the shared core so the operator path
	// and the automated clawback paths carry the same rule.
	invLines, err := s.invoices.ListLineItems(ctx, tenantID, input.InvoiceID)
	if err != nil {
		return domain.CreditNote{}, fmt.Errorf("list invoice line items: %w", err)
	}
	for _, li := range invLines {
		if li.IsCommitLine() {
			return domain.CreditNote{}, errs.InvalidState(
				"this invoice funds a prepaid commit — credit notes are not supported on commit invoices; void the unpaid invoice to cancel the commit instead")
		}
	}

	// Calculate totals. Line amounts are interpreted as tax-inclusive
	// (gross) so the caller's sum matches the invoice's gross total the
	// caps below use as the ceiling. The tax portion is back-solved from
	// the invoice's tax ratio (see proportional tax block below) so a CN
	// that refunds half the invoice also reverses half the tax.
	var subtotal int64
	for _, line := range input.Lines {
		subtotal += line.Quantity * line.UnitAmountCents
	}

	// Line rows are built up front and committed in the SAME transaction as
	// the header (CreateUnderInvoiceLock) — pre-fix they were inserted one by
	// one after the header committed, so a partial failure left an orphan
	// credit note with a non-zero total and zero lines.
	lineRows := make([]domain.CreditNoteLineItem, 0, len(input.Lines))
	for _, line := range input.Lines {
		lineRows = append(lineRows, domain.CreditNoteLineItem{
			InvoiceLineItemID: line.InvoiceLineItemID,
			Description:       line.Description,
			Quantity:          line.Quantity,
			UnitAmountCents:   line.UnitAmountCents,
			AmountCents:       line.Quantity * line.UnitAmountCents,
		})
	}

	// Build + insert the credit note under a per-invoice advisory lock so the
	// over-credit / over-refund caps are evaluated against a snapshot that
	// can't change underneath us. Two concurrent Create calls for the same
	// invoice serialize on the lock: the second sees the first's note in
	// `existingCNs` and is capped correctly, closing the TOCTOU where both
	// read the same pre-state and both inserted past the invoice total.
	buildFn := func(existingCNs []domain.CreditNote) (domain.CreditNote, error) {
		// Validate: total credit notes cannot exceed invoice total.
		// For unpaid invoices, also cap at current amount_due.
		var existingTotal, existingTaxReversed int64
		for _, cn := range existingCNs {
			if cn.Status != domain.CreditNoteVoided {
				existingTotal += cn.TotalCents
				existingTaxReversed += cn.TaxAmountCents
			}
		}
		if existingTotal+subtotal > inv.TotalAmountCents {
			remaining := inv.TotalAmountCents - existingTotal
			if remaining <= 0 {
				return domain.CreditNote{}, errs.InvalidState("invoice has already been fully credited")
			}
			return domain.CreditNote{}, errs.Invalid("lines", fmt.Sprintf("credit note amount exceeds remaining creditable amount (%.2f)", float64(remaining)/100))
		}
		// For unpaid invoices, credit note cannot exceed current amount_due
		if inv.Status == domain.InvoiceFinalized && subtotal > inv.AmountDueCents {
			return domain.CreditNote{}, errs.Invalid("lines", fmt.Sprintf("credit note amount (%.2f) exceeds amount due (%.2f)", float64(subtotal)/100, float64(inv.AmountDueCents)/100))
		}

		return s.buildCreditNote(ctx, tenantID, input, inv, subtotal, existingTotal, existingTaxReversed, existingCNs)
	}

	var cn domain.CreditNote
	if tx != nil {
		// Coordinator-owned tx: the credit note commits atomically with the
		// caller's other writes (e.g. a subscription item delete).
		cn, err = s.store.CreateUnderInvoiceLockTx(ctx, tx, tenantID, input.InvoiceID, lineRows, buildFn)
	} else {
		cn, err = s.store.CreateUnderInvoiceLock(ctx, tenantID, input.InvoiceID, lineRows, buildFn)
	}
	if err != nil {
		return domain.CreditNote{}, err
	}

	return cn, nil
}

// CreateAndIssueAdjustment creates and immediately issues an adjustment credit
// note that reduces an unpaid, finalized invoice's amount_due by grossCents
// (a tax-inclusive amount). The billing engine calls it on mid-period
// subscription cancel to settle an unpaid in-advance invoice down to the
// consumed portion (#22): the unconsumed prebill is removed from the
// receivable, and because the invoice is unpaid no customer-balance credit or
// card refund is produced — Issue's unpaid branch simply lowers amount_due and
// reverses the proportional output tax. The note is a single gross-amount
// line; Create caps grossCents at the invoice's current amount_due. Returns the
// issued credit note. Idempotency is the caller's responsibility (BillOnCancel
// runs once per cancel — see its contract); a second call would create a second
// adjustment and is rejected by Create's amount_due cap once the first lands.
func (s *Service) CreateAndIssueAdjustment(ctx context.Context, tenantID, invoiceID string, grossCents int64, reason, description string) (domain.CreditNote, error) {
	// Calls the create() CORE directly (not the public Create) so the automated
	// ADR-050 unpaid-source clawback BYPASSES the operator in-flight 409 gate in
	// Create. Unlike the operator (who is told to settle/cancel the charge and
	// retry), the automated cancel/downgrade has no human to bounce to, so it
	// must proceed — but it DEFERS rather than reduces: Issue() leaves the draft
	// unissued while the source is in-flight (ADR-059), and the reconciler issues
	// it once the source settles. issue_pending=true makes that deferred draft
	// (and any post-commit Issue() failure) recoverable by RetryPendingClawbackIssue.
	cn, err := s.create(ctx, tenantID, CreateInput{
		InvoiceID: invoiceID,
		Reason:    reason,
		Lines: []CreditLineInput{{
			Description:     description,
			Quantity:        1,
			UnitAmountCents: grossCents,
		}},
		// Engine-issued under the clock-pinned sub's bound effective-now —
		// so issued_at is in the invoice's (possibly simulated) time domain.
		// ANDed with inv.IsSimulated in buildCreditNote.
		IsSimulated:  true,
		IssuePending: true,
	}, nil)
	if err != nil {
		return domain.CreditNote{}, err
	}
	return s.Issue(ctx, tenantID, cn.ID)
}

// CreateAdjustmentDraftTx creates the clawback adjustment credit note as a
// DRAFT inside the CALLER's transaction (coordinator-owned *sql.Tx, ADR-056),
// marked issue_pending. It commits atomically with the caller's other writes
// (e.g. a subscription item delete), so a credit-note failure rolls back the
// item change — closing the post-commit fire-and-forget gap where a removed
// item could leave the customer un-credited. The caller issues it post-commit
// via Issue(); if that fails the note stays draft+issue_pending and
// RetryPendingClawbackIssue re-issues it. Returns the DRAFT (not yet issued),
// so the caller can collect its id for the post-commit Issue.
func (s *Service) CreateAdjustmentDraftTx(ctx context.Context, tx *sql.Tx, tenantID, invoiceID string, grossCents int64, reason, description string) (domain.CreditNote, error) {
	return s.create(ctx, tenantID, CreateInput{
		InvoiceID: invoiceID,
		Reason:    reason,
		Lines: []CreditLineInput{{
			Description:     description,
			Quantity:        1,
			UnitAmountCents: grossCents,
		}},
		// Engine-issued under the clock-pinned sub's bound effective-now.
		IsSimulated:  true,
		IssuePending: true,
	}, tx)
}

// RetryPendingClawbackIssue re-issues auto-clawback drafts whose post-commit
// Issue() NEVER RAN — the note is still status='draft' AND issue_pending (the
// common case: a crash or transient error in the post-commit window before
// Issue() flipped the status). Cross-tenant + scoped to the ctx's livemode,
// mirroring RetryPendingTaxCommit. Because the scan requires status='draft',
// re-issue is safe by construction: nothing has applied yet, so Issue() runs
// fresh — no double-reverse, no double-credit. Once Issue() succeeds the note
// leaves status='draft' and drops out of the scan.
//
// No partial-INTERNAL-issue window to recover (ADR-061): Issue() is a coordinator
// tx — the draft→issued CAS AND the internal effect (balance credit / amount_due
// reduction) commit together, so a failed internal effect rolls the flip back and
// the note stays status='draft' for this scan to re-issue. The only post-commit
// leg is the EXTERNAL tax reversal, which on failure sets tax_reversal_pending and
// self-heals via RetryPendingCreditNoteTaxReversal — not something this sweep
// chases. Per-row errors are collected, not aborted-on.
func (s *Service) RetryPendingClawbackIssue(ctx context.Context, batch int) (int, []error) {
	livemode := postgres.Livemode(ctx)
	drafts, err := s.store.ListPendingClawbackDrafts(ctx, batch, livemode)
	if err != nil {
		return 0, []error{fmt.Errorf("list pending clawback drafts: %w", err)}
	}
	var errsOut []error
	issued := 0
	for _, cn := range drafts {
		if _, err := s.Issue(ctx, cn.TenantID, cn.ID); err != nil {
			errsOut = append(errsOut, fmt.Errorf("re-issue clawback credit note %s: %w", cn.ID, err))
			continue
		}
		issued++
	}
	return issued, errsOut
}

// RetryPendingCreditNoteTaxReversal re-drives the POST-COMMIT upstream tax
// reversal for issued credit notes whose inline attempt failed
// (tax_reversal_pending). It is the CN-path counterpart to the invoice #310
// RetryPendingTaxReversal: #310 scans voided/uncollectible invoices and keys off
// invoices.tax_reversed_at, so a CN reversal on a FINALIZED/PAID invoice (which
// stamps credit_notes.tax_transaction_id instead) is structurally invisible to
// it — leaving the tenant over-remitting until recovered. Cross-tenant + scoped
// to the ctx's livemode. Re-drive is Stripe-idempotent via the per-CN
// velox_tax_rev_<cn.ID> reference; on success the marker is cleared and the
// reversal transaction id stamped. Per-row errors are collected, not aborted-on.
func (s *Service) RetryPendingCreditNoteTaxReversal(ctx context.Context, batch int) (int, []error) {
	if s.taxRev == nil {
		return 0, nil
	}
	livemode := postgres.Livemode(ctx)
	pending, err := s.store.ListPendingCreditNoteTaxReversal(ctx, batch, livemode)
	if err != nil {
		return 0, []error{fmt.Errorf("list pending credit-note tax reversals: %w", err)}
	}
	var errsOut []error
	reversed := 0
	for _, cn := range pending {
		// Already reversed upstream (a prior sweep stamped the tx id but failed
		// to clear the marker): just clear it so it leaves the scan.
		if cn.TaxTransactionID != "" {
			if err := s.store.SetTaxReversalPending(ctx, cn.TenantID, cn.ID, false); err != nil {
				errsOut = append(errsOut, fmt.Errorf("clear tax_reversal_pending for %s: %w", cn.ID, err))
				continue
			}
			reversed++
			continue
		}
		inv, err := s.invoices.Get(ctx, cn.TenantID, cn.InvoiceID)
		if err != nil {
			errsOut = append(errsOut, fmt.Errorf("get invoice for pending tax reversal %s: %w", cn.ID, err))
			continue
		}
		if inv.TaxTransactionID == "" {
			// No upstream transaction to reverse (provider changed / legacy) —
			// nothing to recover; clear the marker so it leaves the scan.
			if err := s.store.SetTaxReversalPending(ctx, cn.TenantID, cn.ID, false); err != nil {
				errsOut = append(errsOut, fmt.Errorf("clear stale tax_reversal_pending for %s: %w", cn.ID, err))
			}
			continue
		}
		res, err := s.taxRev.ReverseTax(ctx, cn.TenantID, tax.ReversalRequest{
			OriginalTransactionID: inv.TaxTransactionID,
			CreditNoteID:          cn.ID,
			InvoiceID:             cn.InvoiceID,
			Mode:                  tax.ReversalModePartial,
			GrossAmountCents:      cn.TotalCents,
		})
		if err != nil {
			errsOut = append(errsOut, fmt.Errorf("re-drive tax reversal for credit note %s: %w", cn.ID, err))
			continue
		}
		if res != nil && res.TransactionID != "" {
			if err := s.store.SetTaxTransaction(ctx, cn.TenantID, cn.ID, res.TransactionID); err != nil {
				errsOut = append(errsOut, fmt.Errorf("persist reversal tx for %s: %w", cn.ID, err))
				continue
			}
		}
		if err := s.store.SetTaxReversalPending(ctx, cn.TenantID, cn.ID, false); err != nil {
			errsOut = append(errsOut, fmt.Errorf("clear tax_reversal_pending for %s: %w", cn.ID, err))
			continue
		}
		reversed++
	}
	return reversed, errsOut
}

// buildCreditNote runs the tax-breakout, three-channel allocation, refund cap,
// and number generation, returning the credit note to insert. Called inside
// CreateUnderInvoiceLock with the lock-stable `existingCNs` snapshot.
func (s *Service) buildCreditNote(ctx context.Context, tenantID string, input CreateInput, inv domain.Invoice, subtotal, existingTotal, existingTaxReversed int64, existingCNs []domain.CreditNote) (domain.CreditNote, error) {
	// Break out proportional tax from the gross subtotal. The invoice's
	// tax_amount / total_amount ratio tells us what fraction of the gross
	// was tax; apply the same ratio to the CN's gross subtotal so a
	// partial credit reverses the same fraction of tax the invoice
	// originally collected. Zero-tax invoices (tax_amount==0 or no
	// provider) produce zero tax on the CN, preserving legacy behaviour.
	//
	// Cumulative residual true-up (2026-05-25). This is a SEQUENTIAL/temporal
	// reconciliation, distinct from the per-line apportionment in ADR-046:
	// integer-division proportional tax accumulates floor() residuals across
	// multiple CNs issued over time, so the sum of per-CN tax can fall short
	// of the invoice tax by up to (N−1) cents. Because the CNs are issued one
	// at a time (not distributed across peer lines at once), the correct fix
	// is a true-up on the LAST event: the CN that EXHAUSTS the invoice
	// (cumulative CN total reaches the invoice total) absorbs the residual so
	// the cumulative tax reversed equals the original tax exactly. Non-
	// exhausting CNs use the pure proportional formula. The persisted
	// `tax_amount_cents` on the CN drives both the dashboard display and the
	// upstream Stripe Tax reversal Reference shape, so both surfaces converge
	// on the same total.
	var taxAmount, netSubtotal int64
	netSubtotal = subtotal
	if inv.TotalAmountCents > 0 && inv.TaxAmountCents > 0 {
		if existingTotal+subtotal == inv.TotalAmountCents {
			// This CN exhausts the invoice — absorb the residual.
			taxAmount = inv.TaxAmountCents - existingTaxReversed
		} else {
			taxAmount = inv.TaxAmountCents * subtotal / inv.TotalAmountCents
		}
		netSubtotal = subtotal - taxAmount
	}

	// Three-channel allocation (Stripe + Lago shape, verified
	// 2026-05-24). The operator allocates the CN total across:
	//   refund_amount_cents     → Stripe refund to the original PM
	//   credit_amount_cents     → restored to customer's credit balance
	//   out_of_band_amount_cents → handled externally (cash, ACH,
	//                              manual adjustment); audit-trail only
	// The three values must sum to subtotal. New integrations should
	// set the explicit fields. The legacy RefundType ("refund" |
	// "credit") is honored when the new fields are all zero — the
	// equivalent single-channel allocation is filled in.
	//
	// Refund cap: refund_amount_cents ≤ AmountPaidCents − already
	// refunded by prior CNs. Velox cannot push more cash back to
	// the PM than the customer actually paid via card.
	//
	// Default when paid invoice + all three zero + no legacy
	// RefundType: full credit to balance (safest — no cash
	// movement, restorable later).
	var refundAmount, creditAmount, outOfBandAmount int64
	if inv.Status == domain.InvoicePaid {
		refundAmount = input.RefundAmountCents
		creditAmount = input.CreditAmountCents
		outOfBandAmount = input.OutOfBandAmountCents

		// Legacy compatibility: if the caller used the old
		// RefundType field and left all three explicit amounts
		// zero, translate into the equivalent single-channel
		// allocation. Removes the field's surface area without
		// breaking existing SDK callers mid-flight.
		if refundAmount == 0 && creditAmount == 0 && outOfBandAmount == 0 {
			switch input.RefundType {
			case "refund":
				refundAmount = subtotal
			case "credit", "":
				creditAmount = subtotal
			}
		}

		if refundAmount < 0 || creditAmount < 0 || outOfBandAmount < 0 {
			return domain.CreditNote{}, errs.Invalid("allocation", "refund_amount_cents, credit_amount_cents, and out_of_band_amount_cents must all be ≥ 0")
		}
		if refundAmount+creditAmount+outOfBandAmount != subtotal {
			return domain.CreditNote{}, errs.Invalid("allocation", fmt.Sprintf(
				"refund_amount_cents (%.2f) + credit_amount_cents (%.2f) + out_of_band_amount_cents (%.2f) = %.2f, must equal credit note total %.2f",
				float64(refundAmount)/100, float64(creditAmount)/100, float64(outOfBandAmount)/100,
				float64(refundAmount+creditAmount+outOfBandAmount)/100, float64(subtotal)/100,
			))
		}

		// Refund cap: cannot push more cash to PM than was paid
		// via PM (less any prior refunds). Matches Stripe — its
		// `refund_amount` validation rejects over-refund with
		// `amount_too_large`.
		var existingRefunds int64
		for _, cn := range existingCNs {
			if cn.Status == domain.CreditNoteVoided {
				continue
			}
			existingRefunds += cn.RefundAmountCents
		}
		pmRefundable := max(0, inv.AmountPaidCents-existingRefunds)
		if refundAmount > pmRefundable {
			return domain.CreditNote{}, errs.Invalid("refund_amount_cents", fmt.Sprintf(
				"refund amount (%.2f) exceeds payment-method refundable (%.2f) — %.2f paid via card, %.2f already refunded by prior credit notes. Reduce refund_amount_cents and route the rest to credit_amount_cents or out_of_band_amount_cents.",
				float64(refundAmount)/100,
				float64(pmRefundable)/100,
				float64(inv.AmountPaidCents)/100,
				float64(existingRefunds)/100,
			))
		}
	}

	// Credit-note numbers are a strictly monotonic per-tenant sequence, same
	// contract as invoice numbers. No fallback: the previous synthesized
	// CN-YYYYMM-<unixnano%1e6> number was non-monotonic and collision-prone,
	// and swallowing the allocator error hid the misconfiguration — a
	// duplicate document number corrupts accounting downstream, while a
	// failed Create just retries.
	if s.numbers == nil {
		return domain.CreditNote{}, fmt.Errorf("credit-note number generator not wired (call SetNumberGenerator)")
	}
	cnNumber, err := s.numbers.NextCreditNoteNumber(ctx, tenantID)
	if err != nil {
		return domain.CreditNote{}, fmt.Errorf("allocate credit note number: %w", err)
	}
	if cnNumber == "" {
		return domain.CreditNote{}, fmt.Errorf("credit-note number generator returned an empty number")
	}

	return domain.CreditNote{
		InvoiceID:            input.InvoiceID,
		CustomerID:           inv.CustomerID,
		CreditNoteNumber:     cnNumber,
		Status:               domain.CreditNoteDraft,
		Reason:               strings.TrimSpace(input.Reason),
		SubtotalCents:        netSubtotal,
		TaxAmountCents:       taxAmount,
		TotalCents:           subtotal,
		RefundAmountCents:    refundAmount,
		CreditAmountCents:    creditAmount,
		OutOfBandAmountCents: outOfBandAmount,
		Currency:             inv.Currency,
		RefundStatus:         domain.RefundNone,
		// Simulated iff issued under the invoice's bound clock (engine path,
		// input.IsSimulated) AND the invoice itself is simulated. Operator
		// HTTP issuance (input.IsSimulated=false) is always wall-clock.
		IsSimulated: input.IsSimulated && inv.IsSimulated,
		// IssuePending: an auto-issue clawback draft to be issued post-commit and
		// retried by the reconciler if Issue() fails (migration 0121). Only the
		// in-tx clawback create (CreateAdjustmentDraftTx) sets this.
		IssuePending: input.IssuePending,
	}, nil
}

// RefundInput describes a direct refund against a paid invoice. It bypasses
// the usual line-item ceremony by synthesizing a single-line credit note from
// the caller's amount + reason. Used by the invoice /refund endpoint (FEAT-2).
type RefundInput struct {
	InvoiceID   string
	AmountCents int64 // 0 = refund the full remaining refundable amount
	Reason      string
	Description string // optional line-item description; defaults based on reason
}

// CreateRefund creates and issues a refund credit note for a paid invoice in
// one call. This is the "direct refund" entry point exposed at
// POST /invoices/{id}/refund — operators who just want to issue a refund don't
// need to know about credit notes as a data model.
//
// Delegates all validation (invoice must be paid, refund must not exceed paid
// amount, must not exceed invoice total) to Service.Create by shaping a
// synthetic CreateInput. On Issue failure, voids the draft credit note so the
// caller sees a clean rollback rather than an orphan draft.
func (s *Service) CreateRefund(ctx context.Context, tenantID string, input RefundInput) (domain.CreditNote, error) {
	if input.InvoiceID == "" {
		return domain.CreditNote{}, errs.Required("invoice_id")
	}
	if strings.TrimSpace(input.Reason) == "" {
		return domain.CreditNote{}, errs.Required("reason")
	}

	inv, err := s.invoices.Get(ctx, tenantID, input.InvoiceID)
	if err != nil {
		return domain.CreditNote{}, fmt.Errorf("invoice not found: %w", err)
	}
	if inv.Status != domain.InvoicePaid {
		return domain.CreditNote{}, errs.InvalidState("can only refund paid invoices")
	}
	if inv.StripePaymentIntentID == "" {
		return domain.CreditNote{}, errs.InvalidState("invoice has no associated payment to refund")
	}

	// Default amount to the remaining refundable balance. Mirrors the
	// Service.Create cap so the caller gets the same error if they ask for
	// more than is left, but lets them omit amount_cents entirely for the
	// common "refund everything" case.
	amount := input.AmountCents
	if amount == 0 {
		existing, err := s.store.List(ctx, ListFilter{TenantID: tenantID, InvoiceID: input.InvoiceID})
		if err != nil {
			return domain.CreditNote{}, fmt.Errorf("list existing credit notes: %w", err)
		}
		var existingRefunds int64
		for _, cn := range existing {
			if cn.Status != domain.CreditNoteVoided && cn.RefundAmountCents > 0 {
				existingRefunds += cn.RefundAmountCents
			}
		}
		amount = inv.AmountPaidCents - existingRefunds
		if amount <= 0 {
			return domain.CreditNote{}, errs.InvalidState("invoice has already been fully refunded")
		}
	}
	if amount <= 0 {
		return domain.CreditNote{}, errs.Invalid("amount_cents", "must be greater than 0")
	}

	description := strings.TrimSpace(input.Description)
	if description == "" {
		description = "Refund: " + strings.TrimSpace(input.Reason)
	}

	cn, err := s.Create(ctx, tenantID, CreateInput{
		InvoiceID:         input.InvoiceID,
		Reason:            input.Reason,
		RefundAmountCents: amount,
		Lines: []CreditLineInput{{
			Description:     description,
			Quantity:        1,
			UnitAmountCents: amount,
		}},
	})
	if err != nil {
		return domain.CreditNote{}, err
	}

	issued, err := s.Issue(ctx, tenantID, cn.ID)
	if err != nil {
		// Issue failed after Create succeeded — void the draft so we don't
		// leave an unusable record. Best-effort: Void deliberately refuses
		// when the Stripe refund leg already executed (that cash must stay
		// counted in the over-refund cap), and any other void failure is
		// non-actionable here. Either way, surface the original issue error.
		_, _ = s.Void(ctx, tenantID, cn.ID)
		return domain.CreditNote{}, err
	}
	return issued, nil
}

func (s *Service) Get(ctx context.Context, tenantID, id string) (domain.CreditNote, error) {
	return s.store.Get(ctx, tenantID, id)
}

// GetWithLineItems fetches a credit note and its line items in one call.
// Used by the PDF download handler and any UI view that needs both.
func (s *Service) GetWithLineItems(ctx context.Context, tenantID, id string) (domain.CreditNote, []domain.CreditNoteLineItem, error) {
	cn, err := s.store.Get(ctx, tenantID, id)
	if err != nil {
		return domain.CreditNote{}, nil, err
	}
	items, err := s.store.ListLineItems(ctx, tenantID, id)
	if err != nil {
		return domain.CreditNote{}, nil, err
	}
	return cn, items, nil
}

func (s *Service) List(ctx context.Context, filter ListFilter) ([]domain.CreditNote, error) {
	return s.store.List(ctx, filter)
}

// CreditedCents returns the sum of non-voided credit-note totals already issued
// against an invoice — i.e. how much of the invoice has been credited. The
// remaining creditable headroom is inv.TotalAmountCents - CreditedCents. The
// billing engine's headroom-aware multi-invoice credit fan-out uses it so a
// share never overruns an invoice whose creditable amount a prior credit note
// (e.g. an earlier downgrade clawback) already consumed.
func (s *Service) CreditedCents(ctx context.Context, tenantID, invoiceID string) (int64, error) {
	cns, err := s.store.List(ctx, ListFilter{TenantID: tenantID, InvoiceID: invoiceID})
	if err != nil {
		return 0, err
	}
	var total int64
	for _, cn := range cns {
		if cn.Status != domain.CreditNoteVoided {
			total += cn.TotalCents
		}
	}
	return total, nil
}

func (s *Service) Issue(ctx context.Context, tenantID, id string) (domain.CreditNote, error) {
	cn, err := s.store.Get(ctx, tenantID, id)
	if err != nil {
		return domain.CreditNote{}, err
	}
	if cn.Status != domain.CreditNoteDraft {
		return domain.CreditNote{}, errs.InvalidState("can only issue draft credit notes")
	}

	// Read the source invoice BEFORE the CAS so the two source-state gates below
	// can decide whether to issue at all without claiming the transition.
	inv, err := s.invoices.Get(ctx, tenantID, cn.InvoiceID)
	if err != nil {
		return domain.CreditNote{}, fmt.Errorf("get invoice: %w", err)
	}

	// Orphan guard. The source was annulled (voided) or written off
	// (uncollectible) after this draft was created — Void already reversed the
	// invoice's tax and zeroed its receivable, so there is nothing left to
	// relieve. Issuing now would (unpaid branch) re-reverse the same tax
	// transaction (double under-remit) against a terminal invoice. Void the
	// draft so it leaves the reconciler scan and never applies. Pure status
	// flip on a never-applied draft — no money movement, no tax call.
	if inv.Status == domain.InvoiceVoided || inv.Status == domain.InvoiceUncollectible {
		if _, terr := s.store.TransitionStatus(ctx, tenantID, id, domain.CreditNoteDraft, domain.CreditNoteVoided); terr != nil {
			return domain.CreditNote{}, fmt.Errorf("void orphaned clawback draft (source %s %s): %w", cn.InvoiceID, inv.Status, terr)
		}
		slog.InfoContext(ctx, "voided orphaned clawback draft (source annulled before issue)",
			"credit_note_id", cn.ID, "invoice_id", cn.InvoiceID, "source_status", string(inv.Status))
		return s.store.Get(ctx, tenantID, id)
	}

	// Defer-until-settle gate (ADR-059). The source's charge is still in flight
	// (processing/unknown) — its captured amount is unknown. Issuing now would
	// either reduce amount_due before MarkPaid records the captured amount
	// (under-record / undersized refund cap) or relieve a charge that may yet
	// succeed. Leave the draft status='draft' + issue_pending so the reconciler
	// re-drives it once the source reaches a terminal payment state, at which
	// point the paid/unpaid branch below selects the correct channel. This is
	// the single chokepoint for ALL issue triggers (engine CreateAndIssueAdjustment,
	// the post-commit issueClawbackDrafts, and the reconciler), so the automated
	// clawback paths defer here rather than each gating their own create site.
	if inv.PaymentStatus.IsInFlight() {
		slog.InfoContext(ctx, "deferring clawback issue until source payment settles",
			"credit_note_id", cn.ID, "invoice_id", cn.InvoiceID, "payment_status", string(inv.PaymentStatus))
		return cn, nil
	}

	// Coordinator transaction (ADR-056 / ADR-061). The draft→issued CAS and the
	// INTERNAL money effect — amount_due reduction (unpaid) or credit grant
	// (paid) — commit ATOMICALLY on this one tx:
	//   - crash mid-tx  → both roll back; the note stays 'draft' and the
	//     reconciler (RetryPendingClawbackIssue) re-drives cleanly.
	//   - crash post-commit → the note is 'issued' AND the effect is applied
	//     together; a re-entry loses the CAS (won=false) and never re-applies.
	// So the reduction/grant is idempotent BY CONSTRUCTION — there is exactly
	// ONE caller of each (here), gated by the CAS, so a second application is
	// impossible without a second CAS win, which cannot happen. (This is why
	// PR2 carries NO source-dedup row; ADR-061 records it as the deferred first
	// brick of the amount_due-derived-ledger. A reviewer adding a second caller
	// of ApplyCreditNote/GrantForCreditNote MUST reintroduce the dedup.)
	// External effects (Stripe refund, upstream tax reversal) cannot share a DB
	// tx, so they run POST-commit, idempotency-keyed and recoverable.
	tx, err := s.store.BeginTx(ctx, tenantID)
	if err != nil {
		return domain.CreditNote{}, fmt.Errorf("begin issue tx: %w", err)
	}
	defer postgres.Rollback(tx)

	won, err := s.store.TransitionStatusTx(ctx, tx, tenantID, id, domain.CreditNoteDraft, domain.CreditNoteIssued)
	if err != nil {
		return domain.CreditNote{}, fmt.Errorf("claim issue transition: %w", err)
	}
	if !won {
		// A concurrent/retried Issue() already claimed the transition. Return
		// the current credit note rather than re-applying side effects.
		return s.store.Get(ctx, tenantID, id)
	}

	if inv.PaymentStatus == domain.PaymentSucceeded {
		// Re-derive the three-channel allocation from the CURRENT invoice
		// state. The allocation persisted at Create is frozen against the
		// invoice status AT THAT TIME: a CN created while the invoice was
		// still unpaid (InvoiceFinalized) skips Create's paid-invoice
		// block entirely, so all three channels land at zero. If that
		// invoice is then paid BEFORE the CN is issued, this Issue path
		// sees a paid invoice but a CN with zero allocations — neither the
		// refund leg nor the credit-grant leg fires, and the refund/credit
		// silently vanishes (the CN issues as a no-op). Mirror Create's
		// paid-invoice default: grant the full CN total to credit balance.
		// Persisted IN the coordinator tx so it commits with the CAS.
		if cn.RefundAmountCents == 0 && cn.CreditAmountCents == 0 && cn.OutOfBandAmountCents == 0 && cn.TotalCents > 0 {
			cn.CreditAmountCents = cn.TotalCents
			if err := s.store.UpdateAllocationTx(ctx, tx, tenantID, id, cn.RefundAmountCents, cn.CreditAmountCents, cn.OutOfBandAmountCents); err != nil {
				return domain.CreditNote{}, fmt.Errorf("re-derive credit-note allocation at issue: %w", err)
			}
		}

		// INTERNAL effect — grant credit to the customer's balance IN the
		// coordinator tx (ADR-061). A grant failure rolls the draft→issued CAS
		// back together, so the issued-but-ungranted orphan (formerly a deferred
		// liveness gap) structurally cannot exist. The Stripe refund leg is
		// EXTERNAL and runs post-commit below.
		if cn.CreditAmountCents > 0 && s.credits != nil {
			if err := s.credits.GrantForCreditNoteTx(ctx, tx, tenantID, cn.ID, CreditGrantInput{
				CustomerID:  cn.CustomerID,
				AmountCents: cn.CreditAmountCents,
				Description: fmt.Sprintf("Credit note %s — %s", cn.CreditNoteNumber, cn.Reason),
				InvoiceID:   cn.InvoiceID,
			}); err != nil {
				return domain.CreditNote{}, fmt.Errorf("grant credit: %w", err)
			}
		}
	} else {
		// Invoice not yet paid — reduce amount_due IN the coordinator tx. The
		// draft→issued CAS guarantees exactly one caller reaches here per credit
		// note, so the reduction is idempotent by construction (see the tx
		// comment above): a concurrent/retried Issue() can't double-reduce.
		if _, err := s.invoices.ApplyCreditNoteTx(ctx, tx, tenantID, cn.InvoiceID, cn.TotalCents); err != nil {
			return domain.CreditNote{}, fmt.Errorf("reduce invoice amount: %w", err)
		}
	}

	// Commit the CAS + internal effect atomically. From here the credit note is
	// durably issued; the remaining steps are EXTERNAL and recover independently.
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return domain.CreditNote{}, fmt.Errorf("commit issue tx: %w", err)
		}
	}

	// ===== POST-COMMIT external effects (idempotency-keyed, recoverable) =====

	// Paid-branch Stripe refund (EXTERNAL — cannot share the DB tx, per "no
	// network I/O in a DB transaction"). Idempotency contract: the deterministic
	// `velox_cn_<cn_id>` key makes a retry after a partial failure return the
	// original refund_id rather than double-refund; refund_status is persisted
	// immediately, and a failed/pending leg recovers via RetryRefund.
	if inv.PaymentStatus == domain.PaymentSucceeded && cn.RefundAmountCents > 0 && cn.StripeRefundID == "" {
		if s.refunder != nil && inv.StripePaymentIntentID != "" {
			idempotencyKey := fmt.Sprintf("velox_cn_%s", cn.ID)
			refundID, refStatus, err := s.refunder.CreateRefund(ctx, inv.StripePaymentIntentID, cn.RefundAmountCents, idempotencyKey)
			if err != nil {
				slog.Warn("stripe refund failed, credit note will be issued with failed refund status",
					"credit_note_id", cn.ID, "error", err)
				cn.RefundStatus = domain.RefundFailed
			} else {
				cn.StripeRefundID = refundID
				// Record what Stripe actually said — succeeded OR pending (async).
				// A pending refund settles later via the refund webhook; recording
				// a blanket "succeeded" here is the false-success bug this fixes.
				cn.RefundStatus = refStatus
			}
		} else {
			cn.RefundStatus = domain.RefundPending
		}
		if cn.RefundStatus != domain.RefundNone {
			if err := s.store.UpdateRefundStatus(ctx, tenantID, id, cn.RefundStatus, cn.StripeRefundID); err != nil {
				slog.Warn("failed to persist refund status",
					"credit_note_id", cn.ID, "refund_status", cn.RefundStatus,
					"stripe_refund_id", cn.StripeRefundID, "error", err)
			}
		}
	}

	// Reverse the invoice's upstream tax liability so the tenant's tax reports
	// reflect the reduced revenue (EU VAT Directive Art. 90, UK VATA 1994, India
	// CGST §34). EXTERNAL call → post-commit. Preconditions: taxRev wired, the
	// invoice has a committed upstream transaction, and this CN hasn't already
	// reversed. Partial mode with the CN's gross total so multi-CN invoices each
	// reverse only their slice.
	//
	// On failure this is NO LONGER fire-and-forget: set tax_reversal_pending so
	// RetryPendingCreditNoteTaxReversal re-drives with the same per-CN key, and
	// raise the log to ERROR (the #310 void-path sibling's alertable signal).
	// #310 itself cannot recover this — it scans voided/uncollectible invoices,
	// while a CN reversal lands on a finalized/paid invoice and stamps
	// credit_notes.tax_transaction_id, structurally invisible to that sweep.
	if s.taxRev != nil && inv.TaxTransactionID != "" && cn.TaxTransactionID == "" && cn.TotalCents > 0 {
		res, err := s.taxRev.ReverseTax(ctx, tenantID, tax.ReversalRequest{
			OriginalTransactionID: inv.TaxTransactionID,
			CreditNoteID:          cn.ID,
			InvoiceID:             cn.InvoiceID,
			Mode:                  tax.ReversalModePartial,
			GrossAmountCents:      cn.TotalCents,
		})
		if err != nil {
			slog.ErrorContext(ctx, "tax reversal failed; marked pending for sweep recovery",
				"credit_note_id", cn.ID,
				"invoice_id", cn.InvoiceID,
				"tax_transaction_id", inv.TaxTransactionID,
				"error", err)
			if merr := s.store.SetTaxReversalPending(ctx, tenantID, id, true); merr != nil {
				// Even if this fast-path marker write fails, the sweep still
				// recovers the reversal: RetryPendingCreditNoteTaxReversal also
				// derives eligibility structurally (issued CN, no reversal stamped,
				// tax-bearing source), so this is not a lost-recovery window.
				slog.ErrorContext(ctx, "failed to set tax_reversal_pending marker; sweep recovers structurally regardless",
					"credit_note_id", cn.ID, "error", merr)
			}
		} else if res != nil && res.TransactionID != "" {
			if err := s.store.SetTaxTransaction(ctx, tenantID, id, res.TransactionID); err != nil {
				slog.Warn("tax reversal succeeded upstream but local persist failed",
					"credit_note_id", cn.ID,
					"reversal_transaction_id", res.TransactionID,
					"error", err)
			}
		}
	}

	// Status was already flipped to issued by the CAS; return the current row
	// (with the refund/tax fields persisted above).
	return s.store.Get(ctx, tenantID, id)
}

// RetryRefund re-attempts the Stripe refund for an already-issued
// credit note whose refund_status landed at `failed` (Stripe rejected
// or network errored at Issue time) or `pending` (no refunder
// configured / no PI at Issue time, e.g. Stripe was wired up
// afterwards). The credit-note itself stays issued — only the Stripe-
// refund leg retries.
//
// Industry parity: Stripe's own `credit_notes.refunds` array is
// append-only; refunds have their own status independent of the CN's
// status. Velox tracks a single refund per CN (no array), but the
// retry contract matches: the CN is a financial document that stays
// in its final state while the cash-back leg can be re-driven by the
// operator.
//
// Idempotency: uses the same `velox_cn_<id>` key as Issue() so a
// retry after a previous-attempt-network-failure-but-Stripe-succeeded
// scenario converges — Stripe returns the existing refund_id and
// Velox persists it without double-charging the tenant.
//
// Rejects when:
//   - CN status != issued (drafts use Issue(); voided CNs are terminal)
//   - refund_status not in (failed, pending) — succeeded is final
//   - refund_amount_cents == 0 (credit-only CN; no Stripe leg exists)
//   - refunder not wired (Stripe not connected)
//   - invoice has no PaymentIntent (paid via credits only)
func (s *Service) RetryRefund(ctx context.Context, tenantID, id string) (domain.CreditNote, error) {
	cn, err := s.store.Get(ctx, tenantID, id)
	if err != nil {
		return domain.CreditNote{}, err
	}
	if cn.Status != domain.CreditNoteIssued {
		return domain.CreditNote{}, errs.InvalidState(fmt.Sprintf(
			"can only retry refund on issued credit notes (current status: %s)", cn.Status))
	}
	switch cn.RefundStatus {
	case domain.RefundFailed, domain.RefundPending:
		// retry-eligible
	case domain.RefundSucceeded:
		return domain.CreditNote{}, errs.InvalidState("refund already succeeded — nothing to retry")
	default:
		return domain.CreditNote{}, errs.InvalidState(fmt.Sprintf(
			"cannot retry refund with status %q (refund leg did not run on this CN)", cn.RefundStatus))
	}
	if cn.RefundAmountCents <= 0 {
		return domain.CreditNote{}, errs.InvalidState("credit-only credit note has no refund leg to retry")
	}
	if s.refunder == nil {
		return domain.CreditNote{}, errs.InvalidState("refunder not configured — connect Stripe before retrying refund")
	}
	inv, err := s.invoices.Get(ctx, tenantID, cn.InvoiceID)
	if err != nil {
		return domain.CreditNote{}, fmt.Errorf("get invoice: %w", err)
	}
	if inv.StripePaymentIntentID == "" {
		return domain.CreditNote{}, errs.InvalidState("invoice has no PaymentIntent — refund cannot be processed via Stripe")
	}

	// Same idempotency key as Issue() — Stripe dedups against this so
	// the retry is safe even if the prior attempt actually succeeded
	// (network failure after Stripe completed): Stripe returns the
	// existing refund_id, Velox persists it.
	idempotencyKey := fmt.Sprintf("velox_cn_%s", cn.ID)
	refundID, refStatus, refundErr := s.refunder.CreateRefund(ctx, inv.StripePaymentIntentID, cn.RefundAmountCents, idempotencyKey)
	if refundErr != nil {
		// Persist the still-failed state so the next retry has the
		// latest error context and the dashboard surfaces accurate
		// status.
		_ = s.store.UpdateRefundStatus(ctx, tenantID, id, domain.RefundFailed, cn.StripeRefundID)
		return domain.CreditNote{}, fmt.Errorf("stripe refund retry: %w", refundErr)
	}

	// Record Stripe's actual status — a re-driven refund can come back `pending`
	// (still settling), not necessarily succeeded; the refund webhook settles it.
	// Recording a blanket succeeded here would re-introduce the false-success lie
	// and permanently 409 a legitimate later retry.
	if err := s.store.UpdateRefundStatus(ctx, tenantID, id, refStatus, refundID); err != nil {
		// Stripe call succeeded but local persist failed. The
		// idempotency key on the next retry converges (Stripe
		// returns same refund_id, local persist runs again).
		return domain.CreditNote{}, fmt.Errorf("persist refund status: %w", err)
	}

	return s.store.Get(ctx, tenantID, id)
}

// ApplyRefundWebhook applies an async refund-webhook status (already mapped to a
// Velox refund_status by the payment layer) to the credit note carrying
// stripeRefundID, monotonically (terminal wins; a stale 'pending' never clobbers
// a terminal). This is the source of truth for the async refund outcome
// (pending→succeeded/failed) that the create-call cannot observe. Returns
// ErrNotFound for an unknown/foreign refund id — the caller decides ack vs retry.
func (s *Service) ApplyRefundWebhook(ctx context.Context, tenantID, stripeRefundID string, status domain.RefundStatus) error {
	return s.store.ApplyRefundWebhookStatus(ctx, tenantID, stripeRefundID, status)
}

func (s *Service) Void(ctx context.Context, tenantID, id string) (domain.CreditNote, error) {
	cn, err := s.store.Get(ctx, tenantID, id)
	if err != nil {
		return domain.CreditNote{}, err
	}
	if cn.Status == domain.CreditNoteVoided {
		return domain.CreditNote{}, errs.InvalidState("credit note is already voided")
	}
	// Industry standard: only draft credit notes can be voided.
	// Once issued, a credit note is a financial document — side effects
	// (invoice reduction, credit grants, Stripe refunds) have already occurred.
	// To correct a mistake on an issued CN, create a new invoice or adjustment.
	if cn.Status == domain.CreditNoteIssued {
		return domain.CreditNote{}, errs.InvalidState("cannot void an issued credit note — issued credit notes are final financial documents")
	}
	// Refuse to void a draft CN whose Stripe refund leg already executed.
	// CreateRefund() voids the draft as a best-effort rollback when Issue()
	// returns an error — but Issue() persists the Stripe refund_id BEFORE
	// the credit-grant / tax-reversal steps, so an error after the refund
	// leg succeeded leaves a draft CN that ALREADY pushed cash back to the
	// payment method. The over-refund cap in Create()/CreateRefund() sums
	// RefundAmountCents only over non-voided CNs, so voiding this CN would
	// drop its executed refund from that ceiling — a later CN could then
	// refund the same money again (double refund). Keep the CN un-voided so
	// it stays counted; the operator reconciles the partially-issued CN
	// (e.g. RetryRefund / re-Issue) instead.
	if cn.StripeRefundID != "" || cn.RefundStatus == domain.RefundSucceeded {
		return domain.CreditNote{}, errs.InvalidState("cannot void a credit note whose refund has already been processed — voiding would drop the executed refund from the over-refund cap and allow a duplicate refund. Reconcile the existing refund instead.")
	}
	return s.store.UpdateStatus(ctx, tenantID, id, domain.CreditNoteVoided)
}
