package invoice

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
)

type memStore struct {
	invoices  map[string]domain.Invoice
	lineItems map[string][]domain.InvoiceLineItem
}

func newMemStore() *memStore {
	return &memStore{
		invoices:  make(map[string]domain.Invoice),
		lineItems: make(map[string][]domain.InvoiceLineItem),
	}
}

func (m *memStore) Create(_ context.Context, tenantID string, inv domain.Invoice) (domain.Invoice, error) {
	inv.ID = fmt.Sprintf("vlx_inv_%d", len(m.invoices)+1)
	inv.TenantID = tenantID
	now := time.Now().UTC()
	inv.CreatedAt = now
	inv.UpdatedAt = now
	m.invoices[inv.ID] = inv
	return inv, nil
}

func (m *memStore) Get(_ context.Context, tenantID, id string) (domain.Invoice, error) {
	inv, ok := m.invoices[id]
	if !ok || inv.TenantID != tenantID {
		return domain.Invoice{}, errs.ErrNotFound
	}
	return inv, nil
}

func (m *memStore) GetByNumber(_ context.Context, tenantID, number string) (domain.Invoice, error) {
	for _, inv := range m.invoices {
		if inv.TenantID == tenantID && inv.InvoiceNumber == number {
			return inv, nil
		}
	}
	return domain.Invoice{}, errs.ErrNotFound
}

func (m *memStore) List(_ context.Context, filter ListFilter) ([]domain.Invoice, int, error) {
	var result []domain.Invoice
	for _, inv := range m.invoices {
		if inv.TenantID != filter.TenantID {
			continue
		}
		if filter.Status != "" && string(inv.Status) != filter.Status {
			continue
		}
		result = append(result, inv)
	}
	return result, len(result), nil
}

func (m *memStore) UpdateStatus(_ context.Context, tenantID, id string, status domain.InvoiceStatus) (domain.Invoice, error) {
	inv, ok := m.invoices[id]
	if !ok || inv.TenantID != tenantID {
		return domain.Invoice{}, errs.ErrNotFound
	}
	inv.Status = status
	inv.UpdatedAt = time.Now().UTC()
	m.invoices[id] = inv
	return inv, nil
}

func (m *memStore) UpdatePayment(_ context.Context, tenantID, id string, ps domain.InvoicePaymentStatus, stripeID, errMsg string, paidAt *time.Time) (domain.Invoice, error) {
	inv, ok := m.invoices[id]
	if !ok || inv.TenantID != tenantID {
		return domain.Invoice{}, errs.ErrNotFound
	}
	inv.PaymentStatus = ps
	inv.StripePaymentIntentID = stripeID
	inv.LastPaymentError = errMsg
	inv.PaidAt = paidAt
	m.invoices[id] = inv
	return inv, nil
}

func (m *memStore) ApplyCreditNote(_ context.Context, tenantID, id string, amountCents int64) (domain.Invoice, error) {
	inv, ok := m.invoices[id]
	if !ok || inv.TenantID != tenantID {
		return domain.Invoice{}, errs.ErrNotFound
	}
	inv.AmountDueCents -= amountCents
	if inv.AmountDueCents < 0 {
		inv.AmountDueCents = 0
	}
	m.invoices[id] = inv
	return inv, nil
}

func (m *memStore) CreateLineItem(_ context.Context, tenantID string, item domain.InvoiceLineItem) (domain.InvoiceLineItem, error) {
	item.ID = fmt.Sprintf("vlx_ili_%d", len(m.lineItems[item.InvoiceID])+1)
	item.TenantID = tenantID
	m.lineItems[item.InvoiceID] = append(m.lineItems[item.InvoiceID], item)
	return item, nil
}

func (m *memStore) ListLineItems(_ context.Context, _, invoiceID string) ([]domain.InvoiceLineItem, error) {
	return m.lineItems[invoiceID], nil
}

func TestCreate(t *testing.T) {
	svc := NewService(newMemStore())
	ctx := context.Background()

	t.Run("valid", func(t *testing.T) {
		inv, err := svc.Create(ctx, "t1", CreateInput{
			CustomerID:         "cus_1",
			SubscriptionID:     "sub_1",
			BillingPeriodStart: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
			BillingPeriodEnd:   time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if inv.Status != domain.InvoiceDraft {
			t.Errorf("got status %q, want draft", inv.Status)
		}
		if inv.PaymentStatus != domain.PaymentPending {
			t.Errorf("got payment_status %q, want pending", inv.PaymentStatus)
		}
		if inv.Currency != "USD" {
			t.Errorf("got currency %q, want USD", inv.Currency)
		}
		if inv.NetPaymentTermDays != 30 {
			t.Errorf("got net_payment_term %d, want 30", inv.NetPaymentTermDays)
		}
		if inv.InvoiceNumber == "" {
			t.Error("invoice_number should be generated")
		}
		if inv.IssuedAt == nil {
			t.Error("issued_at should be set")
		}
		if inv.DueAt == nil {
			t.Error("due_at should be set")
		}
	})

	t.Run("missing customer_id", func(t *testing.T) {
		_, err := svc.Create(ctx, "t1", CreateInput{SubscriptionID: "s"})
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("missing subscription_id", func(t *testing.T) {
		_, err := svc.Create(ctx, "t1", CreateInput{CustomerID: "c"})
		if err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestFinalizeAndVoid(t *testing.T) {
	svc := NewService(newMemStore())
	ctx := context.Background()

	inv, _ := svc.Create(ctx, "t1", CreateInput{
		CustomerID: "c", SubscriptionID: "s",
		BillingPeriodStart: time.Now(), BillingPeriodEnd: time.Now().AddDate(0, 1, 0),
	})

	t.Run("finalize draft", func(t *testing.T) {
		finalized, err := svc.Finalize(ctx, "t1", inv.ID)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if finalized.Status != domain.InvoiceFinalized {
			t.Errorf("got status %q, want finalized", finalized.Status)
		}
	})

	t.Run("cannot finalize again", func(t *testing.T) {
		_, err := svc.Finalize(ctx, "t1", inv.ID)
		if err == nil {
			t.Fatal("expected error finalizing non-draft")
		}
	})

	t.Run("void finalized", func(t *testing.T) {
		voided, err := svc.Void(ctx, "t1", inv.ID)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if voided.Status != domain.InvoiceVoided {
			t.Errorf("got status %q, want voided", voided.Status)
		}
	})
}

func TestRecordPayment(t *testing.T) {
	svc := NewService(newMemStore())
	ctx := context.Background()

	inv, _ := svc.Create(ctx, "t1", CreateInput{
		CustomerID: "c", SubscriptionID: "s",
		BillingPeriodStart: time.Now(), BillingPeriodEnd: time.Now().AddDate(0, 1, 0),
	})

	t.Run("success", func(t *testing.T) {
		paid, err := svc.RecordPayment(ctx, "t1", inv.ID, "pi_stripe_123")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if paid.PaymentStatus != domain.PaymentSucceeded {
			t.Errorf("got payment_status %q, want succeeded", paid.PaymentStatus)
		}
		if paid.StripePaymentIntentID != "pi_stripe_123" {
			t.Errorf("got stripe_pi %q, want pi_stripe_123", paid.StripePaymentIntentID)
		}
		if paid.PaidAt == nil {
			t.Error("paid_at should be set")
		}
	})

	t.Run("failure", func(t *testing.T) {
		failed, err := svc.RecordPaymentFailure(ctx, "t1", inv.ID, "pi_stripe_456", "card_declined")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if failed.PaymentStatus != domain.PaymentFailed {
			t.Errorf("got payment_status %q, want failed", failed.PaymentStatus)
		}
		if failed.LastPaymentError != "card_declined" {
			t.Errorf("got error %q, want card_declined", failed.LastPaymentError)
		}
	})
}
