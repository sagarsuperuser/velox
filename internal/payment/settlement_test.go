package payment

import (
	"context"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// The webhook tests (stripe_test.go) already pin the primitive via the webhook
// entry point. These exercise it DIRECTLY — calling SettleSucceeded /
// SettleFailed with a non-webhook source — to lock the ADR-049 contract that
// the side-effects are source-independent. This is the foundation Phase 2
// relies on: the reconciler will call these same methods, so a backstop-
// recovered settlement must fire the SAME consequences (dunning, mark) as the
// webhook, and inherit the out-of-order guard.

func TestSettleSucceeded_MarksPaidFromAnySource(t *testing.T) {
	invoices := newMockInvoiceUpdater()
	invoices.invoices["inv_1"] = domain.Invoice{
		ID: "inv_1", TenantID: "t1", Status: domain.InvoiceFinalized,
		PaymentStatus: domain.PaymentProcessing, StripePaymentIntentID: "pi_abc",
	}
	invoices.byPI["pi_abc"] = "inv_1"

	s := NewStripe(&mockStripeClient{}, invoices, newMockWebhookStore(), nil)

	// Call as the reconciler would (not via the webhook).
	if err := s.SettleSucceeded(context.Background(), "t1", invoices.invoices["inv_1"], "pi_abc", SourceReconciler); err != nil {
		t.Fatalf("SettleSucceeded: %v", err)
	}

	inv := invoices.invoices["inv_1"]
	if inv.PaymentStatus != domain.PaymentSucceeded || inv.Status != domain.InvoicePaid {
		t.Errorf("status: got payment=%q invoice=%q, want succeeded/paid", inv.PaymentStatus, inv.Status)
	}
	if inv.PaidAt == nil {
		t.Error("paid_at must be set")
	}
}

func TestSettleFailed_FiresDunningFromAnySource(t *testing.T) {
	invoices := newMockInvoiceUpdater()
	invoices.invoices["inv_1"] = domain.Invoice{
		ID: "inv_1", TenantID: "t1", CustomerID: "cus_1", Status: domain.InvoiceFinalized,
		PaymentStatus: domain.PaymentProcessing, StripePaymentIntentID: "pi_def",
	}
	invoices.byPI["pi_def"] = "inv_1"
	dunning := &recordingDunningStarter{}

	s := NewStripe(&mockStripeClient{}, invoices, newMockWebhookStore(), nil, dunning)

	// A reconciler-style direct call (suppressCustomerEmail=false): this is the
	// convergence Phase 2 depends on — the primitive fires dunning regardless
	// of who discovered the failure, so a dropped-webhook recovery is not a
	// silent under-collection.
	if err := s.SettleFailed(context.Background(), "t1", invoices.invoices["inv_1"], "pi_def", "Your card was declined.", false, SourceReconciler); err != nil {
		t.Fatalf("SettleFailed: %v", err)
	}

	inv := invoices.invoices["inv_1"]
	if inv.PaymentStatus != domain.PaymentFailed {
		t.Errorf("payment_status: got %q, want failed", inv.PaymentStatus)
	}
	if inv.LastPaymentError != "Your card was declined." {
		t.Errorf("error: got %q, want the decline message", inv.LastPaymentError)
	}
	if len(dunning.calls) != 1 || dunning.calls[0].invoiceID != "inv_1" {
		t.Fatalf("StartDunning calls: got %+v, want exactly one for inv_1 (dunning must fire from any source)", dunning.calls)
	}
}

func TestSettleFailed_OutOfOrderGuardLivesInPrimitive(t *testing.T) {
	paidAt := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	invoices := newMockInvoiceUpdater()
	invoices.invoices["inv_1"] = domain.Invoice{
		ID: "inv_1", TenantID: "t1", Status: domain.InvoicePaid,
		PaymentStatus: domain.PaymentSucceeded, PaidAt: &paidAt,
		StripePaymentIntentID: "pi_ok",
	}
	dunning := &recordingDunningStarter{}

	s := NewStripe(&mockStripeClient{}, invoices, newMockWebhookStore(), nil, dunning)

	// A stale failure for an already-paid invoice, arriving via ANY source,
	// must be a no-op — the guard lives in the primitive, so every settler
	// (reconciler included) inherits it.
	if err := s.SettleFailed(context.Background(), "t1", invoices.invoices["inv_1"], "pi_stale", "Your card was declined.", false, SourceReconciler); err != nil {
		t.Fatalf("SettleFailed: %v", err)
	}

	inv := invoices.invoices["inv_1"]
	if inv.PaymentStatus != domain.PaymentSucceeded || inv.PaidAt == nil {
		t.Errorf("out-of-order failure corrupted a paid invoice: payment=%q paid_at=%v", inv.PaymentStatus, inv.PaidAt)
	}
	if inv.StripePaymentIntentID != "pi_ok" {
		t.Errorf("stale PI relinked: got %q, want pi_ok", inv.StripePaymentIntentID)
	}
	if len(dunning.calls) != 0 {
		t.Errorf("dunning started on an already-paid invoice: %+v", dunning.calls)
	}
}
