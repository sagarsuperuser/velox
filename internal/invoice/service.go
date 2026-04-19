package invoice

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/platform/clock"
)

type Service struct {
	store Store
	clock clock.Clock
}

func NewService(store Store, clk clock.Clock) *Service {
	if clk == nil {
		clk = clock.Real()
	}
	return &Service{store: store, clock: clk}
}

type CreateInput struct {
	CustomerID         string    `json:"customer_id"`
	SubscriptionID     string    `json:"subscription_id"`
	Currency           string    `json:"currency"`
	BillingPeriodStart time.Time `json:"billing_period_start"`
	BillingPeriodEnd   time.Time `json:"billing_period_end"`
	NetPaymentTermDays int       `json:"net_payment_term_days"`
	Memo               string    `json:"memo,omitempty"`
}

func (s *Service) Create(ctx context.Context, tenantID string, input CreateInput) (domain.Invoice, error) {
	if input.CustomerID == "" {
		return domain.Invoice{}, fmt.Errorf("customer_id is required")
	}
	if input.SubscriptionID == "" {
		return domain.Invoice{}, fmt.Errorf("subscription_id is required")
	}

	currency := strings.ToUpper(strings.TrimSpace(input.Currency))
	if currency == "" {
		currency = "USD"
	}

	netDays := input.NetPaymentTermDays
	if netDays <= 0 {
		netDays = 30
	}

	now := s.clock.Now()
	invoiceNumber := generateInvoiceNumber(now)
	issuedAt := now
	dueAt := now.AddDate(0, 0, netDays)

	return s.store.Create(ctx, tenantID, domain.Invoice{
		CustomerID:         input.CustomerID,
		SubscriptionID:     input.SubscriptionID,
		InvoiceNumber:      invoiceNumber,
		Status:             domain.InvoiceDraft,
		PaymentStatus:      domain.PaymentPending,
		Currency:           currency,
		BillingPeriodStart: input.BillingPeriodStart,
		BillingPeriodEnd:   input.BillingPeriodEnd,
		IssuedAt:           &issuedAt,
		DueAt:              &dueAt,
		NetPaymentTermDays: netDays,
		Memo:               strings.TrimSpace(input.Memo),
	})
}

func (s *Service) Get(ctx context.Context, tenantID, id string) (domain.Invoice, error) {
	return s.store.Get(ctx, tenantID, id)
}

func (s *Service) List(ctx context.Context, filter ListFilter) ([]domain.Invoice, int, error) {
	return s.store.List(ctx, filter)
}

func (s *Service) Finalize(ctx context.Context, tenantID, id string) (domain.Invoice, error) {
	inv, err := s.store.Get(ctx, tenantID, id)
	if err != nil {
		return domain.Invoice{}, err
	}
	if inv.Status != domain.InvoiceDraft {
		return domain.Invoice{}, fmt.Errorf("can only finalize draft invoices, current status: %s", inv.Status)
	}
	return s.store.UpdateStatus(ctx, tenantID, id, domain.InvoiceFinalized)
}

func (s *Service) Void(ctx context.Context, tenantID, id string) (domain.Invoice, error) {
	inv, err := s.store.Get(ctx, tenantID, id)
	if err != nil {
		return domain.Invoice{}, err
	}
	if inv.Status == domain.InvoicePaid {
		return domain.Invoice{}, fmt.Errorf("cannot void a paid invoice — issue a credit note instead")
	}
	if inv.Status == domain.InvoiceVoided {
		return domain.Invoice{}, fmt.Errorf("invoice is already voided")
	}
	return s.store.UpdateStatus(ctx, tenantID, id, domain.InvoiceVoided)
}

func (s *Service) RecordPayment(ctx context.Context, tenantID, id string, stripePaymentIntentID string) (domain.Invoice, error) {
	now := s.clock.Now()
	return s.store.UpdatePayment(ctx, tenantID, id, domain.PaymentSucceeded, stripePaymentIntentID, "", &now)
}

func (s *Service) RecordPaymentFailure(ctx context.Context, tenantID, id, stripePaymentIntentID, errorMessage string) (domain.Invoice, error) {
	return s.store.UpdatePayment(ctx, tenantID, id, domain.PaymentFailed, stripePaymentIntentID, errorMessage, nil)
}

func (s *Service) GetWithLineItems(ctx context.Context, tenantID, id string) (domain.Invoice, []domain.InvoiceLineItem, error) {
	inv, err := s.store.Get(ctx, tenantID, id)
	if err != nil {
		return domain.Invoice{}, nil, err
	}
	items, err := s.store.ListLineItems(ctx, tenantID, id)
	if err != nil {
		return domain.Invoice{}, nil, err
	}
	return inv, items, nil
}

type AddLineItemInput struct {
	Description     string `json:"description"`
	LineType        string `json:"line_type"`
	Quantity        int64  `json:"quantity"`
	UnitAmountCents int64  `json:"unit_amount_cents"`
}

func (s *Service) AddLineItem(ctx context.Context, tenantID, invoiceID string, input AddLineItemInput) (domain.InvoiceLineItem, error) {
	inv, err := s.store.Get(ctx, tenantID, invoiceID)
	if err != nil {
		return domain.InvoiceLineItem{}, err
	}
	if inv.Status != domain.InvoiceDraft {
		return domain.InvoiceLineItem{}, fmt.Errorf("can only add line items to draft invoices, current status: %s", inv.Status)
	}

	desc := strings.TrimSpace(input.Description)
	if desc == "" {
		return domain.InvoiceLineItem{}, fmt.Errorf("description is required")
	}
	if input.Quantity <= 0 {
		return domain.InvoiceLineItem{}, fmt.Errorf("quantity must be greater than 0")
	}
	if input.UnitAmountCents <= 0 {
		return domain.InvoiceLineItem{}, fmt.Errorf("unit_amount_cents must be greater than 0")
	}

	lineType := strings.TrimSpace(input.LineType)
	if lineType == "" {
		lineType = "manual"
	}

	amountCents := input.Quantity * input.UnitAmountCents

	item, err := s.store.CreateLineItem(ctx, tenantID, domain.InvoiceLineItem{
		InvoiceID:        invoiceID,
		LineType:         domain.InvoiceLineItemType(lineType),
		Description:      desc,
		Quantity:         input.Quantity,
		UnitAmountCents:  input.UnitAmountCents,
		AmountCents:      amountCents,
		TotalAmountCents: amountCents,
		Currency:         inv.Currency,
	})
	if err != nil {
		return domain.InvoiceLineItem{}, err
	}

	// Recalculate invoice totals from all line items
	items, err := s.store.ListLineItems(ctx, tenantID, invoiceID)
	if err != nil {
		return item, nil // Line item created but totals not updated — non-fatal
	}

	var subtotal int64
	for _, li := range items {
		subtotal += li.AmountCents
	}
	total := subtotal + inv.TaxAmountCents - inv.DiscountCents
	amountDue := total - inv.AmountPaidCents - inv.CreditsAppliedCents
	if amountDue < 0 {
		amountDue = 0
	}

	_, _ = s.store.UpdateTotals(ctx, tenantID, invoiceID, subtotal, total, amountDue)

	return item, nil
}

func (s *Service) ListApproachingDue(ctx context.Context, daysBeforeDue int) ([]domain.Invoice, error) {
	return s.store.ListApproachingDue(ctx, daysBeforeDue)
}

func generateInvoiceNumber(t time.Time) string {
	return fmt.Sprintf("VLX-%s-%04d", t.Format("200601"), t.UnixMilli()%10000)
}
