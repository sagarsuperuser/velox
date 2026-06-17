package payment

import (
	"context"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
)

func finalizedPendingInvoice() domain.Invoice {
	issued := time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC)
	return domain.Invoice{
		ID: "inv_1", TenantID: "t1", CustomerID: "cus_1",
		Status:             domain.InvoiceFinalized,
		PaymentStatus:      domain.PaymentPending,
		AmountDueCents:     5000,
		TotalAmountCents:   5000,
		Currency:           "USD",
		IssuedAt:           &issued,
		BillingPeriodStart: issued.Add(-30 * 24 * time.Hour),
		BillingPeriodEnd:   issued,
	}
}

// TestChargeInvoice_SyncSuccessSettlesInline locks ADR-049 Phase 3: when Stripe
// returns a `succeeded` PaymentIntent synchronously in the create response (the
// common off-session card case), the charge path settles the invoice INLINE —
// no webhook required. This is the fix for the test-clock stuck-`processing`
// symptom: the invoice resolves in-request rather than waiting on a wall-clock
// webhook.
func TestChargeInvoice_SyncSuccessSettlesInline(t *testing.T) {
	client := &mockStripeClient{piID: "pi_ok", chargeStatus: "succeeded"}
	invoices := newMockInvoiceUpdater()
	invoices.invoices["inv_1"] = finalizedPendingInvoice()
	dunning := &recordingDunningStarter{}
	s := NewStripe(client, invoices, newMockWebhookStore(), nil, dunning)

	got, err := s.ChargeInvoice(context.Background(), "t1", invoices.invoices["inv_1"], "cus_stripe_abc", "pm_test")
	if err != nil {
		t.Fatalf("ChargeInvoice: %v", err)
	}
	// Returned invoice reflects the settled state (callers act on it).
	if got.PaymentStatus != domain.PaymentSucceeded || got.Status != domain.InvoicePaid {
		t.Errorf("returned invoice: got payment=%q status=%q, want succeeded/paid", got.PaymentStatus, got.Status)
	}
	// Persisted state is settled WITHOUT any webhook.
	stored := invoices.invoices["inv_1"]
	if stored.PaymentStatus != domain.PaymentSucceeded || stored.Status != domain.InvoicePaid {
		t.Errorf("stored invoice: got payment=%q status=%q, want succeeded/paid (settled inline, no webhook)", stored.PaymentStatus, stored.Status)
	}
	if stored.PaidAt == nil {
		t.Error("paid_at must be set by the inline settle")
	}
	if stored.StripePaymentIntentID != "pi_ok" {
		t.Errorf("stripe_payment_intent_id: got %q, want pi_ok", stored.StripePaymentIntentID)
	}
	// A successful charge does not start dunning.
	if len(dunning.calls) != 0 {
		t.Errorf("dunning started on a successful charge: %+v", dunning.calls)
	}
}

// TestChargeInvoice_ProcessingStaysProcessing confirms a genuinely in-flight
// status (async methods / off-session SCA) is NOT settled inline — it stays
// `processing` and awaits the webhook (+ the reconciler backstop from Phase 2).
func TestChargeInvoice_ProcessingStaysProcessing(t *testing.T) {
	client := &mockStripeClient{piID: "pi_async", chargeStatus: "processing"}
	invoices := newMockInvoiceUpdater()
	invoices.invoices["inv_1"] = finalizedPendingInvoice()
	s := NewStripe(client, invoices, newMockWebhookStore(), nil)

	got, err := s.ChargeInvoice(context.Background(), "t1", invoices.invoices["inv_1"], "cus_stripe_abc", "pm_test")
	if err != nil {
		t.Fatalf("ChargeInvoice: %v", err)
	}
	if got.PaymentStatus != domain.PaymentProcessing {
		t.Errorf("returned invoice: got %q, want processing", got.PaymentStatus)
	}
	stored := invoices.invoices["inv_1"]
	if stored.PaymentStatus != domain.PaymentProcessing {
		t.Errorf("stored invoice: got %q, want processing (in-flight awaits webhook)", stored.PaymentStatus)
	}
	if stored.Status == domain.InvoicePaid {
		t.Error("an in-flight charge must NOT mark the invoice paid")
	}
	if stored.StripePaymentIntentID != "pi_async" {
		t.Errorf("stripe_payment_intent_id: got %q, want pi_async", stored.StripePaymentIntentID)
	}
}
