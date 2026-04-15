package payment

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
)

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

type mockStripeClient struct {
	lastParams  PaymentIntentParams
	shouldFail  bool
	piID        string
}

func (m *mockStripeClient) CreatePaymentIntent(_ context.Context, params PaymentIntentParams) (PaymentIntentResult, error) {
	m.lastParams = params
	if m.shouldFail {
		return PaymentIntentResult{}, fmt.Errorf("card_declined")
	}
	return PaymentIntentResult{
		ID:           m.piID,
		Status:       "requires_capture",
		ClientSecret: "pi_secret_test",
	}, nil
}

func (m *mockStripeClient) CancelPaymentIntent(_ context.Context, _ string) error {
	return nil
}

type mockInvoiceUpdater struct {
	invoices map[string]domain.Invoice
	byPI     map[string]string // PI ID -> invoice ID
}

func newMockInvoiceUpdater() *mockInvoiceUpdater {
	return &mockInvoiceUpdater{
		invoices: make(map[string]domain.Invoice),
		byPI:     make(map[string]string),
	}
}

func (m *mockInvoiceUpdater) UpdatePayment(_ context.Context, tenantID, id string, ps domain.InvoicePaymentStatus, piID, errMsg string, paidAt *time.Time) (domain.Invoice, error) {
	inv, ok := m.invoices[id]
	if !ok {
		return domain.Invoice{}, errs.ErrNotFound
	}
	inv.PaymentStatus = ps
	inv.StripePaymentIntentID = piID
	inv.LastPaymentError = errMsg
	inv.PaidAt = paidAt
	m.invoices[id] = inv
	if piID != "" {
		m.byPI[piID] = id
	}
	return inv, nil
}

func (m *mockInvoiceUpdater) UpdateStatus(_ context.Context, _, id string, status domain.InvoiceStatus) (domain.Invoice, error) {
	inv, ok := m.invoices[id]
	if !ok {
		return domain.Invoice{}, errs.ErrNotFound
	}
	inv.Status = status
	m.invoices[id] = inv
	return inv, nil
}

func (m *mockInvoiceUpdater) GetByStripePaymentIntentID(_ context.Context, _, piID string) (domain.Invoice, error) {
	id, ok := m.byPI[piID]
	if !ok {
		return domain.Invoice{}, errs.ErrNotFound
	}
	return m.invoices[id], nil
}

func (m *mockInvoiceUpdater) Get(_ context.Context, _, id string) (domain.Invoice, error) {
	inv, ok := m.invoices[id]
	if !ok {
		return domain.Invoice{}, errs.ErrNotFound
	}
	return inv, nil
}

func (m *mockInvoiceUpdater) MarkPaid(_ context.Context, _, id string, stripePI string, paidAt time.Time) (domain.Invoice, error) {
	inv, ok := m.invoices[id]
	if !ok {
		return domain.Invoice{}, errs.ErrNotFound
	}
	inv.Status = domain.InvoicePaid
	inv.PaymentStatus = domain.PaymentSucceeded
	inv.StripePaymentIntentID = stripePI
	inv.PaidAt = &paidAt
	inv.AmountDueCents = 0
	m.invoices[id] = inv
	return inv, nil
}

func (m *mockInvoiceUpdater) ApplyCreditNote(_ context.Context, _, id string, amountCents int64) (domain.Invoice, error) {
	inv, ok := m.invoices[id]
	if !ok {
		return domain.Invoice{}, errs.ErrNotFound
	}
	inv.AmountDueCents -= amountCents
	if inv.AmountDueCents < 0 {
		inv.AmountDueCents = 0
	}
	m.invoices[id] = inv
	return inv, nil
}

type mockWebhookStore struct {
	events map[string]bool
}

func newMockWebhookStore() *mockWebhookStore {
	return &mockWebhookStore{events: make(map[string]bool)}
}

func (m *mockWebhookStore) IngestEvent(_ context.Context, _ string, event domain.StripeWebhookEvent) (domain.StripeWebhookEvent, bool, error) {
	if m.events[event.StripeEventID] {
		return event, false, nil
	}
	m.events[event.StripeEventID] = true
	return event, true, nil
}

func (m *mockWebhookStore) ListByInvoice(_ context.Context, _, _ string) ([]domain.StripeWebhookEvent, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestChargeInvoice_Success(t *testing.T) {
	client := &mockStripeClient{piID: "pi_test_123"}
	invoices := newMockInvoiceUpdater()
	webhooks := newMockWebhookStore()

	inv := domain.Invoice{
		ID: "inv_1", TenantID: "t1", CustomerID: "cus_1",
		InvoiceNumber: "VLX-202604-0001",
		Status: domain.InvoiceFinalized, PaymentStatus: domain.PaymentPending,
		Currency: "USD", AmountDueCents: 19900,
	}
	invoices.invoices["inv_1"] = inv

	stripe := NewStripe(client, invoices, webhooks, nil)
	result, err := stripe.ChargeInvoice(context.Background(), "t1", inv, "cus_stripe_abc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify PaymentIntent params
	if client.lastParams.AmountCents != 19900 {
		t.Errorf("amount: got %d, want 19900", client.lastParams.AmountCents)
	}
	if client.lastParams.CustomerID != "cus_stripe_abc" {
		t.Errorf("customer: got %q, want cus_stripe_abc", client.lastParams.CustomerID)
	}
	if client.lastParams.Currency != "USD" {
		t.Errorf("currency: got %q, want USD", client.lastParams.Currency)
	}
	if client.lastParams.Metadata["velox_invoice_id"] != "inv_1" {
		t.Errorf("metadata missing velox_invoice_id")
	}

	// Verify invoice updated to processing
	if result.PaymentStatus != domain.PaymentProcessing {
		t.Errorf("payment_status: got %q, want processing", result.PaymentStatus)
	}
	if result.StripePaymentIntentID != "pi_test_123" {
		t.Errorf("stripe_pi: got %q, want pi_test_123", result.StripePaymentIntentID)
	}
}

func TestChargeInvoice_NotFinalized(t *testing.T) {
	stripe := NewStripe(&mockStripeClient{}, newMockInvoiceUpdater(), newMockWebhookStore(), nil)

	inv := domain.Invoice{Status: domain.InvoiceDraft, AmountDueCents: 100}
	_, err := stripe.ChargeInvoice(context.Background(), "t1", inv, "cus_stripe")
	if err == nil {
		t.Fatal("expected error for non-finalized invoice")
	}
}

func TestChargeInvoice_ZeroAmount(t *testing.T) {
	stripe := NewStripe(&mockStripeClient{}, newMockInvoiceUpdater(), newMockWebhookStore(), nil)

	inv := domain.Invoice{Status: domain.InvoiceFinalized, AmountDueCents: 0}
	_, err := stripe.ChargeInvoice(context.Background(), "t1", inv, "cus_stripe")
	if err == nil {
		t.Fatal("expected error for zero amount")
	}
}

func TestChargeInvoice_StripeFailure(t *testing.T) {
	client := &mockStripeClient{shouldFail: true}
	invoices := newMockInvoiceUpdater()
	invoices.invoices["inv_1"] = domain.Invoice{
		ID: "inv_1", TenantID: "t1", Status: domain.InvoiceFinalized,
		AmountDueCents: 5000, Currency: "USD",
	}

	stripe := NewStripe(client, invoices, newMockWebhookStore(), nil)
	_, err := stripe.ChargeInvoice(context.Background(), "t1", invoices.invoices["inv_1"], "cus_stripe")
	if err == nil {
		t.Fatal("expected error when Stripe fails")
	}

	// Invoice should be marked as failed
	updated := invoices.invoices["inv_1"]
	if updated.PaymentStatus != domain.PaymentFailed {
		t.Errorf("payment_status: got %q, want failed", updated.PaymentStatus)
	}
}

func TestHandleWebhook_PaymentSucceeded(t *testing.T) {
	invoices := newMockInvoiceUpdater()
	invoices.invoices["inv_1"] = domain.Invoice{
		ID: "inv_1", TenantID: "t1", Status: domain.InvoiceFinalized,
		PaymentStatus: domain.PaymentProcessing,
		StripePaymentIntentID: "pi_abc",
	}
	invoices.byPI["pi_abc"] = "inv_1"

	webhooks := newMockWebhookStore()
	stripe := NewStripe(&mockStripeClient{}, invoices, webhooks, nil)

	err := stripe.HandleWebhook(context.Background(), "t1", domain.StripeWebhookEvent{
		StripeEventID:   "evt_001",
		EventType:       "payment_intent.succeeded",
		PaymentIntentID: "pi_abc",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	inv := invoices.invoices["inv_1"]
	if inv.PaymentStatus != domain.PaymentSucceeded {
		t.Errorf("payment_status: got %q, want succeeded", inv.PaymentStatus)
	}
	if inv.Status != domain.InvoicePaid {
		t.Errorf("status: got %q, want paid", inv.Status)
	}
	if inv.PaidAt == nil {
		t.Error("paid_at should be set")
	}
}

func TestHandleWebhook_PaymentFailed(t *testing.T) {
	invoices := newMockInvoiceUpdater()
	invoices.invoices["inv_1"] = domain.Invoice{
		ID: "inv_1", TenantID: "t1", Status: domain.InvoiceFinalized,
		PaymentStatus: domain.PaymentProcessing,
		StripePaymentIntentID: "pi_def",
	}
	invoices.byPI["pi_def"] = "inv_1"

	stripe := NewStripe(&mockStripeClient{}, invoices, newMockWebhookStore(), nil)

	err := stripe.HandleWebhook(context.Background(), "t1", domain.StripeWebhookEvent{
		StripeEventID:   "evt_002",
		EventType:       "payment_intent.payment_failed",
		PaymentIntentID: "pi_def",
		FailureMessage:  "Your card was declined.",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	inv := invoices.invoices["inv_1"]
	if inv.PaymentStatus != domain.PaymentFailed {
		t.Errorf("payment_status: got %q, want failed", inv.PaymentStatus)
	}
	if inv.LastPaymentError != "Your card was declined." {
		t.Errorf("error: got %q, want 'Your card was declined.'", inv.LastPaymentError)
	}
}

func TestHandleWebhook_DuplicateEvent(t *testing.T) {
	invoices := newMockInvoiceUpdater()
	invoices.invoices["inv_1"] = domain.Invoice{
		ID: "inv_1", TenantID: "t1", Status: domain.InvoiceFinalized,
		StripePaymentIntentID: "pi_dup",
	}
	invoices.byPI["pi_dup"] = "inv_1"

	webhooks := newMockWebhookStore()
	stripe := NewStripe(&mockStripeClient{}, invoices, webhooks, nil)

	event := domain.StripeWebhookEvent{
		StripeEventID:   "evt_dup",
		EventType:       "payment_intent.succeeded",
		PaymentIntentID: "pi_dup",
	}

	// First call — processes
	err := stripe.HandleWebhook(context.Background(), "t1", event)
	if err != nil {
		t.Fatalf("first call failed: %v", err)
	}

	// Reset invoice to verify second call doesn't change it
	inv := invoices.invoices["inv_1"]
	inv.PaymentStatus = domain.PaymentProcessing
	inv.Status = domain.InvoiceFinalized
	invoices.invoices["inv_1"] = inv

	// Second call — should be idempotent (duplicate event)
	err = stripe.HandleWebhook(context.Background(), "t1", event)
	if err != nil {
		t.Fatalf("second call failed: %v", err)
	}

	// Invoice should NOT have been updated again
	inv = invoices.invoices["inv_1"]
	if inv.PaymentStatus != domain.PaymentProcessing {
		t.Errorf("duplicate event should not update: got %q", inv.PaymentStatus)
	}
}

func TestHandleWebhook_UnhandledEvent(t *testing.T) {
	stripe := NewStripe(&mockStripeClient{}, newMockInvoiceUpdater(), newMockWebhookStore(), nil)

	err := stripe.HandleWebhook(context.Background(), "t1", domain.StripeWebhookEvent{
		StripeEventID: "evt_other",
		EventType:     "charge.refunded",
	})
	if err != nil {
		t.Fatalf("unhandled events should not error: %v", err)
	}
}
