package creditnote

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
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

	// Calculate totals
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
		SubtotalCents:     subtotal,
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

func (s *Service) Get(ctx context.Context, tenantID, id string) (domain.CreditNote, error) {
	return s.store.Get(ctx, tenantID, id)
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
