package creditnote

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/tax"
)

// InvoiceReader reads invoice data for credit note validation.
type InvoiceReader interface {
	Get(ctx context.Context, tenantID, id string) (domain.Invoice, error)
	ListLineItems(ctx context.Context, tenantID, invoiceID string) ([]domain.InvoiceLineItem, error)
	ApplyCreditNote(ctx context.Context, tenantID, id string, amountCents int64) (domain.Invoice, error)
}

// Refunder processes refunds via the payment provider.
//
// `idempotencyKey` is passed to the provider (Stripe-style) so a retry
// after a partial failure in Issue() returns the existing refund_id
// rather than creating a duplicate refund. Callers MUST pass a
// deterministic key tied to the credit-note id (Velox uses
// `velox_cn_<cn_id>`).
type Refunder interface {
	CreateRefund(ctx context.Context, paymentIntentID string, amountCents int64, idempotencyKey string) (string, error)
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
}

type CreditLineInput struct {
	InvoiceLineItemID string `json:"invoice_line_item_id,omitempty"`
	Description       string `json:"description"`
	Quantity          int64  `json:"quantity"`
	UnitAmountCents   int64  `json:"unit_amount_cents"`
}

func (s *Service) Create(ctx context.Context, tenantID string, input CreateInput) (domain.CreditNote, error) {
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
	cn, err := s.store.CreateUnderInvoiceLock(ctx, tenantID, input.InvoiceID, lineRows, func(existingCNs []domain.CreditNote) (domain.CreditNote, error) {
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
	})
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
	cn, err := s.Create(ctx, tenantID, CreateInput{
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
		IsSimulated: true,
	})
	if err != nil {
		return domain.CreditNote{}, err
	}
	return s.Issue(ctx, tenantID, cn.ID)
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

func (s *Service) Issue(ctx context.Context, tenantID, id string) (domain.CreditNote, error) {
	cn, err := s.store.Get(ctx, tenantID, id)
	if err != nil {
		return domain.CreditNote{}, err
	}
	if cn.Status != domain.CreditNoteDraft {
		return domain.CreditNote{}, errs.InvalidState("can only issue draft credit notes")
	}

	// Compare-and-swap the draft→issued transition BEFORE any side effect.
	// Issue() reduces the invoice amount_due (unpaid path) and grants/refunds
	// (paid path); the amount_due reduction is not idempotent, so two
	// concurrent or retried Issue() calls that both passed the draft check
	// above would apply it twice — double-reducing amount_due. The CAS makes
	// exactly one caller the winner; losers see won=false and return the
	// already-issued credit note unchanged.
	won, err := s.store.TransitionStatus(ctx, tenantID, id, domain.CreditNoteDraft, domain.CreditNoteIssued)
	if err != nil {
		return domain.CreditNote{}, fmt.Errorf("claim issue transition: %w", err)
	}
	if !won {
		// A concurrent/retried Issue() already claimed the transition. Return
		// the current credit note rather than re-applying side effects.
		return s.store.Get(ctx, tenantID, id)
	}

	inv, err := s.invoices.Get(ctx, tenantID, cn.InvoiceID)
	if err != nil {
		return domain.CreditNote{}, fmt.Errorf("get invoice: %w", err)
	}

	if inv.PaymentStatus == domain.PaymentSucceeded {
		// Re-derive the three-channel allocation from the CURRENT invoice
		// state. The allocation persisted at Create is frozen against the
		// invoice status AT THAT TIME: a CN created while the invoice was
		// still unpaid (InvoiceFinalized) skips Create's paid-invoice
		// block entirely, so all three channels land at zero. If that
		// invoice is then paid BEFORE the CN is issued, this Issue path
		// sees a paid invoice but a CN with zero allocations — neither the
		// refund leg nor the credit-grant leg below fires, and the
		// refund/credit silently vanishes (the CN issues as a no-op).
		//
		// Mirror Create's documented paid-invoice default: when the
		// invoice is now paid and the CN carries zero across all three
		// channels, grant the full CN total to the customer's credit
		// balance — the safest fallback (no cash movement, restorable
		// later). Persist it so the dashboard and any retry see the same
		// allocation the Issue path acts on.
		if cn.RefundAmountCents == 0 && cn.CreditAmountCents == 0 && cn.OutOfBandAmountCents == 0 && cn.TotalCents > 0 {
			cn.CreditAmountCents = cn.TotalCents
			if err := s.store.UpdateAllocation(ctx, tenantID, id, cn.RefundAmountCents, cn.CreditAmountCents, cn.OutOfBandAmountCents); err != nil {
				return domain.CreditNote{}, fmt.Errorf("re-derive credit-note allocation at issue: %w", err)
			}
		}

		// Invoice already paid — handle based on refund type.
		//
		// Idempotency contract (added 2026-05-22 after audit):
		//   - Stripe refund: deterministic idempotency key
		//     `velox_cn_<cn_id>` so a retry after a partial failure
		//     hits Stripe's cache and returns the original refund_id
		//     rather than creating a duplicate.
		//   - StripeRefundID is persisted IMMEDIATELY after the Stripe
		//     call succeeds, BEFORE the credit-grant step. So even if
		//     a retry happens after a credit-grant failure, the next
		//     call's `cn.StripeRefundID != ""` guard skips the Stripe
		//     re-call. Stripe's idempotency cache is the second line
		//     of defense.
		//
		// Pre-fix shape (caught in audit): no idempotency key + status
		// persisted last → retry-after-partial-failure double-refunded
		// the customer.
		if cn.RefundAmountCents > 0 && cn.StripeRefundID == "" {
			if s.refunder != nil && inv.StripePaymentIntentID != "" {
				idempotencyKey := fmt.Sprintf("velox_cn_%s", cn.ID)
				refundID, err := s.refunder.CreateRefund(ctx, inv.StripePaymentIntentID, cn.RefundAmountCents, idempotencyKey)
				if err != nil {
					slog.Warn("stripe refund failed, credit note will be issued with failed refund status",
						"credit_note_id", cn.ID, "error", err)
					cn.RefundStatus = domain.RefundFailed
				} else {
					cn.StripeRefundID = refundID
					cn.RefundStatus = domain.RefundSucceeded
				}
			} else {
				cn.RefundStatus = domain.RefundPending
			}
			// Persist refund result IMMEDIATELY (before the credit
			// grant step) so a downstream failure doesn't lose the
			// refund_id. Best-effort: if this fails the in-memory cn
			// still carries refund_id and the Stripe idempotency key
			// covers the duplicate-call case on retry.
			if cn.RefundStatus != domain.RefundNone {
				if err := s.store.UpdateRefundStatus(ctx, tenantID, id, cn.RefundStatus, cn.StripeRefundID); err != nil {
					slog.Warn("failed to persist refund status",
						"credit_note_id", cn.ID, "refund_status", cn.RefundStatus,
						"stripe_refund_id", cn.StripeRefundID, "error", err)
				}
			}
		}

		if cn.CreditAmountCents > 0 && s.credits != nil {
			// Credit-type CN: add to customer's prepaid balance via
			// the dedup-safe GrantForCreditNote path. The partial
			// unique index idx_credit_ledger_credit_note_dedup
			// (migration 0093) enforces one grant per (tenant, CN),
			// so a retry after a downstream-step failure (tax
			// reversal / UpdateStatus) returns the existing grant
			// silently instead of double-crediting the customer.
			if err := s.credits.GrantForCreditNote(ctx, tenantID, cn.ID, CreditGrantInput{
				CustomerID:  cn.CustomerID,
				AmountCents: cn.CreditAmountCents,
				Description: fmt.Sprintf("Credit note %s — %s", cn.CreditNoteNumber, cn.Reason),
				InvoiceID:   cn.InvoiceID,
			}); err != nil {
				return domain.CreditNote{}, fmt.Errorf("grant credit: %w", err)
			}
		}
	} else {
		// Invoice not yet paid — reduce amount_due. This is the
		// non-idempotent step; the draft→issued CAS at the top of Issue
		// guarantees exactly one caller reaches here per credit note, so a
		// concurrent/retried Issue() can't double-reduce amount_due.
		if _, err := s.invoices.ApplyCreditNote(ctx, tenantID, cn.InvoiceID, cn.TotalCents); err != nil {
			return domain.CreditNote{}, fmt.Errorf("reduce invoice amount: %w", err)
		}
	}

	// Reverse the invoice's upstream tax liability so the tenant's Stripe
	// Tax reports (or equivalent provider dashboard) reflect the reduced
	// revenue. Industry standard: EU VAT Directive Art. 90, UK VATA 1994,
	// India CGST §34 all require output-tax reduction when a credit note
	// is issued. Preconditions:
	//   - taxRev wired (skipped in narrow tests and for tenants with no
	//     provider configured)
	//   - invoice has a committed upstream transaction (inv.TaxTransactionID
	//     non-empty — none/manual providers and legacy invoices leave it
	//     empty and silently opt out)
	//   - this CN has not already been reversed (idempotency guard against
	//     retried Issue calls; Stripe also enforces reference uniqueness
	//     on the CN id but the local check avoids the round-trip)
	// Mode is partial with the CN's gross total as the flat amount so
	// multi-CN invoices reverse exactly the slice each CN credits,
	// leaving residual liability on the original transaction for any
	// uncredited portion. Failures are logged but do not unwind the CN —
	// the refund/credit side has already committed and the CN is an
	// accounting document; operators reconcile any stuck reversals
	// manually.
	if s.taxRev != nil && inv.TaxTransactionID != "" && cn.TaxTransactionID == "" && cn.TotalCents > 0 {
		res, err := s.taxRev.ReverseTax(ctx, tenantID, tax.ReversalRequest{
			OriginalTransactionID: inv.TaxTransactionID,
			CreditNoteID:          cn.ID,
			InvoiceID:             cn.InvoiceID,
			Mode:                  tax.ReversalModePartial,
			GrossAmountCents:      cn.TotalCents,
		})
		if err != nil {
			slog.Warn("tax reversal failed, credit note will be issued without upstream reversal",
				"credit_note_id", cn.ID,
				"invoice_id", cn.InvoiceID,
				"tax_transaction_id", inv.TaxTransactionID,
				"error", err)
		} else if res != nil && res.TransactionID != "" {
			if err := s.store.SetTaxTransaction(ctx, tenantID, id, res.TransactionID); err != nil {
				slog.Warn("tax reversal succeeded upstream but local persist failed",
					"credit_note_id", cn.ID,
					"reversal_transaction_id", res.TransactionID,
					"error", err)
			}
		}
	}

	// Status was already flipped to issued by the CAS at the top; just return
	// the current row (with the refund/tax fields persisted above).
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
	refundID, refundErr := s.refunder.CreateRefund(ctx, inv.StripePaymentIntentID, cn.RefundAmountCents, idempotencyKey)
	if refundErr != nil {
		// Persist the still-failed state so the next retry has the
		// latest error context and the dashboard surfaces accurate
		// status.
		_ = s.store.UpdateRefundStatus(ctx, tenantID, id, domain.RefundFailed, cn.StripeRefundID)
		return domain.CreditNote{}, fmt.Errorf("stripe refund retry: %w", refundErr)
	}

	if err := s.store.UpdateRefundStatus(ctx, tenantID, id, domain.RefundSucceeded, refundID); err != nil {
		// Stripe call succeeded but local persist failed. The
		// idempotency key on the next retry converges (Stripe
		// returns same refund_id, local persist runs again).
		return domain.CreditNote{}, fmt.Errorf("persist refund status: %w", err)
	}

	return s.store.Get(ctx, tenantID, id)
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
