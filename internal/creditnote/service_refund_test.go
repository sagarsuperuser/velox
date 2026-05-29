package creditnote

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// fakeRefunder records refund calls so tests can assert the Stripe leg ran
// (or didn't) with the expected amount. The returned refund ID is
// deterministic for easy assertions.
type fakeRefunder struct {
	calls    []fakeRefundCall
	failWith error
}

type fakeRefundCall struct {
	paymentIntentID string
	amountCents     int64
	idempotencyKey  string
}

func (f *fakeRefunder) CreateRefund(_ context.Context, paymentIntentID string, amountCents int64, idempotencyKey string) (string, error) {
	f.calls = append(f.calls, fakeRefundCall{
		paymentIntentID: paymentIntentID,
		amountCents:     amountCents,
		idempotencyKey:  idempotencyKey,
	})
	if f.failWith != nil {
		return "", f.failWith
	}
	return fmt.Sprintf("re_fake_%d", len(f.calls)), nil
}

// setupRefundSvc builds a Service with a paid invoice ready for refund. The
// invoice starts at $100 paid, no existing refunds.
func setupRefundSvc(t *testing.T) (*Service, *memStore, *memInvoiceReader, *fakeRefunder) {
	t.Helper()
	store := newMemStore()
	invoices := &memInvoiceReader{
		invoices: map[string]domain.Invoice{
			"inv_paid": {
				ID:                    "inv_paid",
				TenantID:              "t1",
				CustomerID:            "cus_1",
				Status:                domain.InvoicePaid,
				PaymentStatus:         domain.PaymentSucceeded,
				Currency:              "USD",
				TotalAmountCents:      10000,
				AmountPaidCents:       10000,
				StripePaymentIntentID: "pi_test_123",
			},
		},
	}
	refunder := &fakeRefunder{}
	svc := NewService(store, invoices, refunder)
	return svc, store, invoices, refunder
}

func TestCreateRefund_FullRefund_DefaultAmount(t *testing.T) {
	t.Parallel()
	svc, _, _, refunder := setupRefundSvc(t)

	cn, err := svc.CreateRefund(context.Background(), "t1", RefundInput{
		InvoiceID: "inv_paid",
		Reason:    "requested_by_customer",
	})
	if err != nil {
		t.Fatalf("CreateRefund: %v", err)
	}
	if cn.Status != domain.CreditNoteIssued {
		t.Errorf("status: got %q, want issued", cn.Status)
	}
	if cn.RefundAmountCents != 10000 {
		t.Errorf("refund_amount: got %d, want 10000 (full)", cn.RefundAmountCents)
	}
	if cn.RefundStatus != domain.RefundSucceeded {
		t.Errorf("refund_status: got %q, want succeeded", cn.RefundStatus)
	}
	if cn.StripeRefundID == "" {
		t.Error("stripe_refund_id should be set")
	}
	if len(refunder.calls) != 1 {
		t.Fatalf("refund calls: got %d, want 1", len(refunder.calls))
	}
	if refunder.calls[0].amountCents != 10000 {
		t.Errorf("stripe refund amount: got %d, want 10000", refunder.calls[0].amountCents)
	}
}

func TestCreateRefund_PartialRefund(t *testing.T) {
	t.Parallel()
	svc, _, _, refunder := setupRefundSvc(t)

	cn, err := svc.CreateRefund(context.Background(), "t1", RefundInput{
		InvoiceID:   "inv_paid",
		AmountCents: 2500,
		Reason:      "duplicate",
	})
	if err != nil {
		t.Fatalf("CreateRefund: %v", err)
	}
	if cn.RefundAmountCents != 2500 {
		t.Errorf("refund_amount: got %d, want 2500", cn.RefundAmountCents)
	}
	if refunder.calls[0].amountCents != 2500 {
		t.Errorf("stripe refund amount: got %d, want 2500", refunder.calls[0].amountCents)
	}
}

func TestCreateRefund_DefaultAmountAccountsForPriorRefunds(t *testing.T) {
	t.Parallel()
	svc, _, _, refunder := setupRefundSvc(t)
	ctx := context.Background()

	// First partial refund of 3000
	if _, err := svc.CreateRefund(ctx, "t1", RefundInput{
		InvoiceID: "inv_paid", AmountCents: 3000, Reason: "duplicate",
	}); err != nil {
		t.Fatalf("first refund: %v", err)
	}

	// Second call with default amount should refund the remaining 7000
	cn, err := svc.CreateRefund(ctx, "t1", RefundInput{
		InvoiceID: "inv_paid", Reason: "requested_by_customer",
	})
	if err != nil {
		t.Fatalf("second refund: %v", err)
	}
	if cn.RefundAmountCents != 7000 {
		t.Errorf("refund_amount: got %d, want 7000 (10000 - 3000 prior)", cn.RefundAmountCents)
	}
	if len(refunder.calls) != 2 {
		t.Fatalf("refund calls: got %d, want 2", len(refunder.calls))
	}
	if refunder.calls[1].amountCents != 7000 {
		t.Errorf("second stripe call: got %d, want 7000", refunder.calls[1].amountCents)
	}
}

func TestCreateRefund_AlreadyFullyRefunded(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := setupRefundSvc(t)
	ctx := context.Background()

	if _, err := svc.CreateRefund(ctx, "t1", RefundInput{
		InvoiceID: "inv_paid", Reason: "requested_by_customer",
	}); err != nil {
		t.Fatalf("first refund: %v", err)
	}

	_, err := svc.CreateRefund(ctx, "t1", RefundInput{
		InvoiceID: "inv_paid", Reason: "other",
	})
	if err == nil {
		t.Fatal("expected error after full refund")
	}
}

func TestCreateRefund_UnpaidInvoiceRejected(t *testing.T) {
	t.Parallel()
	svc, _, invoices, _ := setupRefundSvc(t)

	invoices.invoices["inv_unpaid"] = domain.Invoice{
		ID: "inv_unpaid", TenantID: "t1", CustomerID: "cus_1",
		Status: domain.InvoiceFinalized, Currency: "USD",
		TotalAmountCents: 5000, AmountDueCents: 5000,
	}

	_, err := svc.CreateRefund(context.Background(), "t1", RefundInput{
		InvoiceID: "inv_unpaid", Reason: "duplicate",
	})
	if err == nil {
		t.Fatal("expected error for unpaid invoice")
	}
}

func TestCreateRefund_NoPaymentIntentRejected(t *testing.T) {
	t.Parallel()
	svc, _, invoices, _ := setupRefundSvc(t)

	invoices.invoices["inv_no_pi"] = domain.Invoice{
		ID: "inv_no_pi", TenantID: "t1", CustomerID: "cus_1",
		Status: domain.InvoicePaid, PaymentStatus: domain.PaymentSucceeded,
		Currency: "USD", TotalAmountCents: 5000, AmountPaidCents: 5000,
	}

	_, err := svc.CreateRefund(context.Background(), "t1", RefundInput{
		InvoiceID: "inv_no_pi", Reason: "duplicate",
	})
	if err == nil {
		t.Fatal("expected error when no payment intent present")
	}
}

func TestCreateRefund_OverRefundRejected(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := setupRefundSvc(t)

	_, err := svc.CreateRefund(context.Background(), "t1", RefundInput{
		InvoiceID:   "inv_paid",
		AmountCents: 15000, // exceeds amount_paid of 10000
		Reason:      "duplicate",
	})
	if err == nil {
		t.Fatal("expected error refunding more than paid")
	}
}

func TestCreateRefund_MissingInvoiceID(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := setupRefundSvc(t)

	_, err := svc.CreateRefund(context.Background(), "t1", RefundInput{
		Reason: "duplicate",
	})
	if err == nil {
		t.Fatal("expected error for missing invoice_id")
	}
}

func TestCreateRefund_MissingReason(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := setupRefundSvc(t)

	_, err := svc.CreateRefund(context.Background(), "t1", RefundInput{
		InvoiceID: "inv_paid",
	})
	if err == nil {
		t.Fatal("expected error for missing reason")
	}
}

// When Stripe refund fails at Issue time, the service records RefundFailed on
// the credit note but still issues it (the CN is an accounting doc — the
// operator can resolve the Stripe side manually).
func TestCreateRefund_StripeFailure_IssuesWithFailedStatus(t *testing.T) {
	t.Parallel()
	svc, _, _, refunder := setupRefundSvc(t)
	refunder.failWith = errors.New("stripe: card network unreachable")

	cn, err := svc.CreateRefund(context.Background(), "t1", RefundInput{
		InvoiceID: "inv_paid", Reason: "fraudulent",
	})
	if err != nil {
		t.Fatalf("CreateRefund should not bubble Stripe failure: %v", err)
	}
	if cn.Status != domain.CreditNoteIssued {
		t.Errorf("status: got %q, want issued (CN still issued on Stripe failure)", cn.Status)
	}
	if cn.RefundStatus != domain.RefundFailed {
		t.Errorf("refund_status: got %q, want failed", cn.RefundStatus)
	}
	if cn.StripeRefundID != "" {
		t.Errorf("stripe_refund_id: got %q, want empty on failure", cn.StripeRefundID)
	}
}

// TestIssue_PassesIdempotencyKeyToStripe locks in the contract that
// Issue() always passes a deterministic idempotency key to the
// refunder. Without this, a retry after a partial-failure (e.g.
// post-refund credit-grant DB error) would call Stripe again with no
// dedup and create a DUPLICATE refund — customer over-refunded.
//
// Key shape: `velox_cn_<cn_id>`. Same key + same params returns the
// cached response from Stripe.
func TestIssue_PassesIdempotencyKeyToStripe(t *testing.T) {
	svc, _, _, refunder := setupRefundSvc(t)

	cn, err := svc.CreateRefund(context.Background(), "t1", RefundInput{
		InvoiceID: "inv_paid",
		Reason:    "test",
	})
	if err != nil {
		t.Fatalf("CreateRefund: %v", err)
	}

	if len(refunder.calls) != 1 {
		t.Fatalf("expected 1 refund call, got %d", len(refunder.calls))
	}
	expected := "velox_cn_" + cn.ID
	if refunder.calls[0].idempotencyKey != expected {
		t.Errorf("idempotency_key: got %q, want %q", refunder.calls[0].idempotencyKey, expected)
	}
}

// TestRetryRefund exercises the operator-driven retry of a Stripe
// refund leg that failed/was-pending at Issue() time. The CN itself
// stays issued; only the cash-back leg is re-driven. Same idempotency
// key as Issue() so retries after network-failure-but-Stripe-actually-
// succeeded converge cleanly (Stripe returns the existing refund_id).
func TestRetryRefund(t *testing.T) {
	t.Run("failed → succeeded; uses same idempotency key as Issue", func(t *testing.T) {
		svc, store, _, refunder := setupRefundSvc(t)

		cn, err := store.Create(context.Background(), "t1", domain.CreditNote{
			TenantID: "t1", InvoiceID: "inv_paid", CustomerID: "cus_1",
			CreditNoteNumber:  "CN-RETRY-1",
			Status:            domain.CreditNoteDraft,
			Reason:            "retry",
			SubtotalCents:     5000,
			TotalCents:        5000,
			RefundAmountCents: 5000,
			Currency:          "USD",
			RefundStatus:      domain.RefundFailed,
		})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		// Manually transition to issued (bypass Issue() flow so we
		// land at status=issued + refund_status=failed for the retry
		// test).
		stale := store.notes[cn.ID]
		stale.Status = domain.CreditNoteIssued
		store.notes[cn.ID] = stale

		out, err := svc.RetryRefund(context.Background(), "t1", cn.ID)
		if err != nil {
			t.Fatalf("RetryRefund: %v", err)
		}
		if out.RefundStatus != domain.RefundSucceeded {
			t.Errorf("refund_status: got %q want succeeded", out.RefundStatus)
		}
		if out.StripeRefundID == "" {
			t.Error("stripe_refund_id should be set after successful retry")
		}
		if len(refunder.calls) != 1 {
			t.Fatalf("expected 1 refund call, got %d", len(refunder.calls))
		}
		expectedKey := "velox_cn_" + cn.ID
		if refunder.calls[0].idempotencyKey != expectedKey {
			t.Errorf("idempotency_key: got %q want %q (must match Issue() key for Stripe-side dedup)",
				refunder.calls[0].idempotencyKey, expectedKey)
		}
	})

	t.Run("rejects already-succeeded refund", func(t *testing.T) {
		svc, store, _, _ := setupRefundSvc(t)
		cn, _ := store.Create(context.Background(), "t1", domain.CreditNote{
			TenantID: "t1", InvoiceID: "inv_paid", CustomerID: "cus_1",
			CreditNoteNumber:  "CN-OK",
			Status:            domain.CreditNoteIssued,
			RefundAmountCents: 5000,
			Currency:          "USD",
			RefundStatus:      domain.RefundSucceeded,
			StripeRefundID:    "re_prior",
		})
		_, err := svc.RetryRefund(context.Background(), "t1", cn.ID)
		if err == nil {
			t.Error("expected retry on already-succeeded refund to reject")
		}
	})

	t.Run("rejects draft CN", func(t *testing.T) {
		svc, store, _, _ := setupRefundSvc(t)
		cn, _ := store.Create(context.Background(), "t1", domain.CreditNote{
			TenantID: "t1", InvoiceID: "inv_paid", CustomerID: "cus_1",
			CreditNoteNumber:  "CN-DRAFT",
			Status:            domain.CreditNoteDraft,
			RefundAmountCents: 5000,
			Currency:          "USD",
			RefundStatus:      domain.RefundFailed,
		})
		_, err := svc.RetryRefund(context.Background(), "t1", cn.ID)
		if err == nil {
			t.Error("expected retry on draft CN to reject")
		}
	})

	t.Run("rejects credit-only CN (no refund leg)", func(t *testing.T) {
		svc, store, _, _ := setupRefundSvc(t)
		cn, _ := store.Create(context.Background(), "t1", domain.CreditNote{
			TenantID: "t1", InvoiceID: "inv_paid", CustomerID: "cus_1",
			CreditNoteNumber:  "CN-CREDIT-ONLY",
			Status:            domain.CreditNoteIssued,
			RefundAmountCents: 0,
			CreditAmountCents: 5000,
			Currency:          "USD",
			RefundStatus:      domain.RefundNone,
		})
		_, err := svc.RetryRefund(context.Background(), "t1", cn.ID)
		if err == nil {
			t.Error("expected retry on credit-only CN to reject")
		}
	})

	t.Run("rejects when invoice has no PaymentIntent", func(t *testing.T) {
		svc, store, invoices, _ := setupRefundSvc(t)
		invoices.invoices["inv_no_pi"] = domain.Invoice{
			ID: "inv_no_pi", TenantID: "t1", CustomerID: "cus_1",
			Status:           domain.InvoicePaid,
			Currency:         "USD",
			TotalAmountCents: 5000,
		}
		cn, _ := store.Create(context.Background(), "t1", domain.CreditNote{
			TenantID: "t1", InvoiceID: "inv_no_pi", CustomerID: "cus_1",
			CreditNoteNumber:  "CN-NO-PI",
			Status:            domain.CreditNoteIssued,
			RefundAmountCents: 5000,
			Currency:          "USD",
			RefundStatus:      domain.RefundPending,
		})
		_, err := svc.RetryRefund(context.Background(), "t1", cn.ID)
		if err == nil {
			t.Error("expected retry to reject when invoice has no PI")
		}
	})
}

// TestIssue_SkipsStripeRefundIfAlreadyRefunded locks in the retry-
// safety guard: when a CN already has a StripeRefundID stamped on it
// (i.e. a prior Issue() attempt persisted it before failing on a
// downstream step), the retry's Issue() MUST NOT call Stripe again.
// The guard is `cn.StripeRefundID == ""`.
func TestIssue_SkipsStripeRefundIfAlreadyRefunded(t *testing.T) {
	svc, store, invoices, refunder := setupRefundSvc(t)
	_ = invoices

	// Create a draft CN manually with a pre-stamped StripeRefundID,
	// simulating "Stripe refund succeeded on first attempt, persisted,
	// then a downstream step failed and operator is retrying Issue()".
	created, err := store.Create(context.Background(), "t1", domain.CreditNote{
		TenantID: "t1", InvoiceID: "inv_paid", CustomerID: "cus_1",
		CreditNoteNumber:  "CN-RETRY",
		Status:            domain.CreditNoteDraft,
		Reason:            "retry",
		SubtotalCents:     5000,
		TotalCents:        5000,
		RefundAmountCents: 5000,
		CreditAmountCents: 0,
		Currency:          "USD",
		RefundStatus:      domain.RefundSucceeded,
		StripeRefundID:    "re_prior_attempt",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := svc.Issue(context.Background(), "t1", created.ID); err != nil {
		t.Fatalf("Issue: %v", err)
	}

	if len(refunder.calls) != 0 {
		t.Errorf("expected 0 refund calls on retry-with-pre-existing-refund_id, got %d (would have been duplicate Stripe charge)", len(refunder.calls))
	}
}
