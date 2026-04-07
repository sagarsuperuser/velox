package invoice

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
)

type Service struct {
	store Store
}

func NewService(store Store) *Service {
	return &Service{store: store}
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

	now := time.Now().UTC()
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
		return domain.Invoice{}, fmt.Errorf("cannot void a paid invoice")
	}
	return s.store.UpdateStatus(ctx, tenantID, id, domain.InvoiceVoided)
}

func (s *Service) RecordPayment(ctx context.Context, tenantID, id string, stripePaymentIntentID string) (domain.Invoice, error) {
	now := time.Now().UTC()
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

func generateInvoiceNumber(t time.Time) string {
	return fmt.Sprintf("VLX-%s-%04d", t.Format("200601"), t.UnixMilli()%10000)
}
