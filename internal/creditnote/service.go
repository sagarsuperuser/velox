package creditnote

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
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

type CreditGrantInput struct {
	CustomerID  string
	AmountCents int64
	Description string
}

type Service struct {
	store    Store
	invoices InvoiceReader
	refunder Refunder
	credits  CreditGranter
}

func NewService(store Store, invoices InvoiceReader, refunder Refunder, credits ...CreditGranter) *Service {
	s := &Service{store: store, invoices: invoices, refunder: refunder}
	if len(credits) > 0 {
		s.credits = credits[0]
	}
	return s
}

type CreateInput struct {
	InvoiceID  string             `json:"invoice_id"`
	Reason     string             `json:"reason"`
	Lines      []CreditLineInput  `json:"lines"`
	RefundType string             `json:"refund_type"` // "refund" or "credit"
}

type CreditLineInput struct {
	InvoiceLineItemID string `json:"invoice_line_item_id,omitempty"`
	Description       string `json:"description"`
	Quantity          int64  `json:"quantity"`
	UnitAmountCents   int64  `json:"unit_amount_cents"`
}

func (s *Service) Create(ctx context.Context, tenantID string, input CreateInput) (domain.CreditNote, error) {
	if input.InvoiceID == "" {
		return domain.CreditNote{}, fmt.Errorf("invoice_id is required")
	}
	if strings.TrimSpace(input.Reason) == "" {
		return domain.CreditNote{}, fmt.Errorf("reason is required")
	}
	if len(input.Lines) == 0 {
		return domain.CreditNote{}, fmt.Errorf("at least one line item is required")
	}

	for i, line := range input.Lines {
		if line.Quantity <= 0 {
			return domain.CreditNote{}, fmt.Errorf("line %d: quantity must be positive", i+1)
		}
		if line.UnitAmountCents <= 0 {
			return domain.CreditNote{}, fmt.Errorf("line %d: unit_amount_cents must be positive", i+1)
		}
	}

	// Verify invoice exists and is finalized or paid
	inv, err := s.invoices.Get(ctx, tenantID, input.InvoiceID)
	if err != nil {
		return domain.CreditNote{}, fmt.Errorf("invoice not found: %w", err)
	}
	if inv.Status != domain.InvoiceFinalized && inv.Status != domain.InvoicePaid {
		return domain.CreditNote{}, fmt.Errorf("can only create credit notes for finalized or paid invoices")
	}

	// Calculate totals
	var subtotal int64
	for _, line := range input.Lines {
		subtotal += line.Quantity * line.UnitAmountCents
	}

	// Determine refund vs credit
	var refundAmount, creditAmount int64
	if input.RefundType == "refund" && inv.Status == domain.InvoicePaid {
		refundAmount = subtotal
	} else {
		creditAmount = subtotal
	}

	now := time.Now().UTC()
	cnNumber := fmt.Sprintf("VLX-CN-%s-%04d", now.Format("200601"), now.UnixMilli()%10000)

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
		return domain.CreditNote{}, fmt.Errorf("can only issue draft credit notes")
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
				return domain.CreditNote{}, fmt.Errorf("stripe refund failed: %w", err)
			}
			cn.StripeRefundID = refundID
			cn.RefundStatus = domain.RefundSucceeded
		} else if cn.CreditAmountCents > 0 && s.credits != nil {
			// Credit type on paid invoice: add to customer's prepaid balance
			// (not double-counting — invoice is already paid, this is new credit for future use)
			if err := s.credits.Grant(ctx, tenantID, CreditGrantInput{
				CustomerID:  cn.CustomerID,
				AmountCents: cn.CreditAmountCents,
				Description: fmt.Sprintf("Credit note %s — %s", cn.CreditNoteNumber, cn.Reason),
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

	return s.store.UpdateStatus(ctx, tenantID, id, domain.CreditNoteIssued)
}

func (s *Service) Void(ctx context.Context, tenantID, id string) (domain.CreditNote, error) {
	cn, err := s.store.Get(ctx, tenantID, id)
	if err != nil {
		return domain.CreditNote{}, err
	}
	if cn.Status == domain.CreditNoteVoided {
		return domain.CreditNote{}, fmt.Errorf("credit note is already voided")
	}
	return s.store.UpdateStatus(ctx, tenantID, id, domain.CreditNoteVoided)
}
