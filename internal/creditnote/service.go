package creditnote

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

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
type Refunder interface {
	CreateRefund(ctx context.Context, paymentIntentID string, amountCents int64) (string, error)
}

// CreditGranter adds credits to a customer's balance.
type CreditGranter interface {
	Grant(ctx context.Context, tenantID string, input CreditGrantInput) error
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

// CouponRedemptionVoider reverses coupon effects tied to an invoice when
// the invoice is fully credited or refunded. Optional — when nil, Issue
// skips coupon reversal (behaviour pre-FEAT-7). Wired in production to
// coupon.Service.VoidRedemptionsForInvoice.
type CouponRedemptionVoider interface {
	VoidRedemptionsForInvoice(ctx context.Context, tenantID, invoiceID string) (int, error)
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
	store         Store
	invoices      InvoiceReader
	refunder      Refunder
	credits       CreditGranter
	numbers       NumberGenerator
	taxRev        TaxReverser
	couponVoider  CouponRedemptionVoider
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

// SetCouponRedemptionVoider wires coupon reversal on full-credit or
// full-refund credit notes. Optional — when unset, Issue leaves coupon
// redemptions alone (the legacy behaviour, which leaks "once" coupon
// usage through refunds).
func (s *Service) SetCouponRedemptionVoider(v CouponRedemptionVoider) {
	s.couponVoider = v
}

type CreateInput struct {
	InvoiceID  string            `json:"invoice_id"`
	Reason     string            `json:"reason"`
	Lines      []CreditLineInput `json:"lines"`
	RefundType string            `json:"refund_type"` // "refund" or "credit"
	AutoIssue  bool              `json:"auto_issue"`  // create + issue atomically
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

	// Validate: total credit notes cannot exceed invoice total.
	// For unpaid invoices, also cap at current amount_due.
	existingCNs, err := s.store.List(ctx, ListFilter{TenantID: tenantID, InvoiceID: input.InvoiceID})
	if err != nil {
		return domain.CreditNote{}, fmt.Errorf("list existing credit notes: %w", err)
	}
	var existingTotal int64
	for _, cn := range existingCNs {
		if cn.Status != domain.CreditNoteVoided {
			existingTotal += cn.TotalCents
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

	// Break out proportional tax from the gross subtotal. The invoice's
	// tax_amount / total_amount ratio tells us what fraction of the gross
	// was tax; apply the same ratio to the CN's gross subtotal so a
	// partial credit reverses the same fraction of tax the invoice
	// originally collected. Zero-tax invoices (tax_amount==0 or no
	// provider) produce zero tax on the CN, preserving legacy behaviour.
	var taxAmount, netSubtotal int64
	netSubtotal = subtotal
	if inv.TotalAmountCents > 0 && inv.TaxAmountCents > 0 {
		taxAmount = inv.TaxAmountCents * subtotal / inv.TotalAmountCents
		netSubtotal = subtotal - taxAmount
	}

	// For refund-type CNs on paid invoices, cannot refund more than was actually paid via Stripe
	if input.RefundType == "refund" && inv.Status == domain.InvoicePaid && inv.AmountPaidCents > 0 {
		// Calculate already-refunded amount from existing CNs
		var existingRefunds int64
		for _, cn := range existingCNs {
			if cn.Status != domain.CreditNoteVoided && cn.RefundAmountCents > 0 {
				existingRefunds += cn.RefundAmountCents
			}
		}
		maxRefundable := inv.AmountPaidCents - existingRefunds
		if maxRefundable <= 0 {
			return domain.CreditNote{}, errs.InvalidState("invoice has already been fully refunded")
		}
		if subtotal > maxRefundable {
			return domain.CreditNote{}, errs.Invalid("lines", fmt.Sprintf("refund amount (%.2f) exceeds refundable amount (%.2f)", float64(subtotal)/100, float64(maxRefundable)/100))
		}
	}

	// Determine refund vs credit
	// On paid invoices: money goes back as refund or credit to customer balance
	// On unpaid invoices: reduces amount_due directly (no refund/credit fields)
	var refundAmount, creditAmount int64
	if inv.Status == domain.InvoicePaid {
		if input.RefundType == "refund" {
			refundAmount = subtotal
		} else {
			creditAmount = subtotal
		}
	}

	now := time.Now().UTC()
	var cnNumber string
	if s.numbers != nil {
		num, err := s.numbers.NextCreditNoteNumber(ctx, tenantID)
		if err == nil && num != "" {
			cnNumber = num
		}
	}
	if cnNumber == "" {
		// Fallback if number generator not configured
		cnNumber = fmt.Sprintf("CN-%s-%06d", now.Format("200601"), now.UnixNano()%1000000)
	}

	cn, err := s.store.Create(ctx, tenantID, domain.CreditNote{
		InvoiceID:         input.InvoiceID,
		CustomerID:        inv.CustomerID,
		CreditNoteNumber:  cnNumber,
		Status:            domain.CreditNoteDraft,
		Reason:            strings.TrimSpace(input.Reason),
		SubtotalCents:     netSubtotal,
		TaxAmountCents:    taxAmount,
		TotalCents:        subtotal,
		RefundAmountCents: refundAmount,
		CreditAmountCents: creditAmount,
		Currency:          inv.Currency,
		RefundStatus:      domain.RefundNone,
	})
	if err != nil {
		return domain.CreditNote{}, err
	}

	// Create line items
	for _, line := range input.Lines {
		_, err := s.store.CreateLineItem(ctx, tenantID, domain.CreditNoteLineItem{
			CreditNoteID:      cn.ID,
			InvoiceLineItemID: line.InvoiceLineItemID,
			Description:       line.Description,
			Quantity:          line.Quantity,
			UnitAmountCents:   line.UnitAmountCents,
			AmountCents:       line.Quantity * line.UnitAmountCents,
		})
		if err != nil {
			return domain.CreditNote{}, fmt.Errorf("create line item: %w", err)
		}
	}

	return cn, nil
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
		InvoiceID:  input.InvoiceID,
		Reason:     input.Reason,
		RefundType: "refund",
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
		// leave an unusable record. Best-effort: if void itself fails,
		// surface the original issue error (it's the actionable one).
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

	inv, err := s.invoices.Get(ctx, tenantID, cn.InvoiceID)
	if err != nil {
		return domain.CreditNote{}, fmt.Errorf("get invoice: %w", err)
	}

	if inv.PaymentStatus == domain.PaymentSucceeded {
		// Invoice already paid — handle based on refund type
		if cn.RefundAmountCents > 0 && s.refunder != nil && inv.StripePaymentIntentID != "" {
			// Refund type: return money to payment method via Stripe
			refundID, err := s.refunder.CreateRefund(ctx, inv.StripePaymentIntentID, cn.RefundAmountCents)
			if err != nil {
				// Refund failed — still issue the CN (it's an accounting document)
				// but mark refund as failed so operators can resolve manually
				slog.Warn("stripe refund failed, credit note will be issued with failed refund status",
					"credit_note_id", cn.ID, "error", err)
				cn.RefundStatus = domain.RefundFailed
			} else {
				cn.StripeRefundID = refundID
				cn.RefundStatus = domain.RefundSucceeded
			}
		} else if cn.RefundAmountCents > 0 {
			// Refund requested but no refunder or no PI — mark pending for manual resolution
			cn.RefundStatus = domain.RefundPending
		}

		if cn.CreditAmountCents > 0 && s.credits != nil {
			// Credit type on paid invoice: add to customer's prepaid balance
			if err := s.credits.Grant(ctx, tenantID, CreditGrantInput{
				CustomerID:  cn.CustomerID,
				AmountCents: cn.CreditAmountCents,
				Description: fmt.Sprintf("Credit note %s — %s", cn.CreditNoteNumber, cn.Reason),
				InvoiceID:   cn.InvoiceID,
			}); err != nil {
				return domain.CreditNote{}, fmt.Errorf("grant credit: %w", err)
			}
		}
	} else {
		// Invoice not yet paid — reduce amount_due
		if _, err := s.invoices.ApplyCreditNote(ctx, tenantID, cn.InvoiceID, cn.TotalCents); err != nil {
			return domain.CreditNote{}, fmt.Errorf("reduce invoice amount: %w", err)
		}
	}

	// Persist refund status updates before issuing
	if cn.RefundStatus != domain.RefundNone {
		if err := s.store.UpdateRefundStatus(ctx, tenantID, id, cn.RefundStatus, cn.StripeRefundID); err != nil {
			slog.Warn("failed to update refund status", "credit_note_id", cn.ID, "error", err)
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

	// Void any coupon redemptions on the underlying invoice when the CN
	// covers the full invoice total. Partial credits leave redemptions
	// intact — the customer still earned the discount on the slice they
	// paid. Threshold is >= so a CN that over-credits (shouldn't happen
	// under the Create-time cap, but defensive) still triggers reversal.
	if s.couponVoider != nil && inv.TotalAmountCents > 0 && cn.TotalCents >= inv.TotalAmountCents {
		if n, err := s.couponVoider.VoidRedemptionsForInvoice(ctx, tenantID, cn.InvoiceID); err != nil {
			slog.Warn("coupon redemption void failed — credit note still issued",
				"credit_note_id", cn.ID, "invoice_id", cn.InvoiceID, "error", err)
		} else if n > 0 {
			slog.Info("coupon redemptions voided on full credit",
				"credit_note_id", cn.ID, "invoice_id", cn.InvoiceID, "voided_count", n)
		}
	}

	return s.store.UpdateStatus(ctx, tenantID, id, domain.CreditNoteIssued)
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
	return s.store.UpdateStatus(ctx, tenantID, id, domain.CreditNoteVoided)
}
