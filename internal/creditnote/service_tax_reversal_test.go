package creditnote

import (
	"context"
	"errors"
	"testing"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/tax"
)

// fakeTaxReverser records reversal calls and returns a deterministic
// upstream transaction id so tests can assert idempotency + persistence.
type fakeTaxReverser struct {
	calls    []tax.ReversalRequest
	failWith error
	returnID string
}

func (f *fakeTaxReverser) ReverseTax(_ context.Context, _ string, req tax.ReversalRequest) (*tax.ReversalResult, error) {
	f.calls = append(f.calls, req)
	if f.failWith != nil {
		return nil, f.failWith
	}
	id := f.returnID
	if id == "" {
		id = "tx_reversal_fake"
	}
	return &tax.ReversalResult{TransactionID: id}, nil
}

// TestCreate_ProportionalTaxBreakdown verifies Create() splits the gross
// CN subtotal into net + tax using the invoice's tax ratio. Without this,
// the CN would show the gross as net and claim zero tax was collected —
// wrong for any tax-bearing invoice.
func TestCreate_ProportionalTaxBreakdown(t *testing.T) {
	t.Parallel()
	store := newMemStore()
	invoices := &memInvoiceReader{
		invoices: map[string]domain.Invoice{
			"inv_taxed": {
				ID: "inv_taxed", TenantID: "t1", CustomerID: "cus_1",
				Status: domain.InvoiceFinalized, Currency: "USD",
				SubtotalCents:    10000,
				TaxAmountCents:   1800,
				TotalAmountCents: 11800,
				AmountDueCents:   11800,
			},
		},
	}
	svc := NewService(store, invoices, nil)

	// Half refund → should break out half the tax.
	cn, err := svc.Create(context.Background(), "t1", CreateInput{
		InvoiceID: "inv_taxed",
		Reason:    "partial adjustment",
		Lines: []CreditLineInput{
			{Description: "adj", Quantity: 1, UnitAmountCents: 5900},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// taxAmount = 5900 * 1800 / 11800 = 900
	if cn.TaxAmountCents != 900 {
		t.Errorf("tax_amount_cents: got %d, want 900 (5900 * 1800 / 11800)", cn.TaxAmountCents)
	}
	if cn.SubtotalCents != 5000 {
		t.Errorf("subtotal_cents (net): got %d, want 5000 (5900 - 900 tax)", cn.SubtotalCents)
	}
	if cn.TotalCents != 5900 {
		t.Errorf("total_cents (gross): got %d, want 5900 (unchanged gross)", cn.TotalCents)
	}
}

// TestCreate_ZeroTaxInvoiceLeavesZeroTaxOnCN verifies the proportional
// block preserves legacy behaviour when the invoice has no tax: the CN
// reports zero tax and subtotal == total == sum of line amounts. Existing
// tests and integrations that assume this invariant continue to work.
func TestCreate_ZeroTaxInvoiceLeavesZeroTaxOnCN(t *testing.T) {
	t.Parallel()
	store := newMemStore()
	invoices := &memInvoiceReader{
		invoices: map[string]domain.Invoice{
			"inv_notax": {
				ID: "inv_notax", TenantID: "t1", CustomerID: "cus_1",
				Status: domain.InvoiceFinalized, Currency: "USD",
				SubtotalCents:    10000,
				TaxAmountCents:   0,
				TotalAmountCents: 10000,
				AmountDueCents:   10000,
			},
		},
	}
	svc := NewService(store, invoices, nil)

	cn, err := svc.Create(context.Background(), "t1", CreateInput{
		InvoiceID: "inv_notax", Reason: "credit",
		Lines: []CreditLineInput{{Description: "x", Quantity: 1, UnitAmountCents: 3000}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if cn.TaxAmountCents != 0 {
		t.Errorf("tax_amount_cents: got %d, want 0", cn.TaxAmountCents)
	}
	if cn.SubtotalCents != 3000 {
		t.Errorf("subtotal_cents: got %d, want 3000", cn.SubtotalCents)
	}
	if cn.TotalCents != 3000 {
		t.Errorf("total_cents: got %d, want 3000", cn.TotalCents)
	}
}

// TestIssue_TaxReversalOnPaidInvoiceWithTransaction verifies the happy
// path: a paid invoice with a committed tax_transaction triggers a
// partial reversal at the CN's gross total, and the returned upstream id
// is persisted onto the CN.
func TestIssue_TaxReversalOnPaidInvoiceWithTransaction(t *testing.T) {
	t.Parallel()
	store := newMemStore()
	invoices := &memInvoiceReader{
		invoices: map[string]domain.Invoice{
			"inv_paid": {
				ID: "inv_paid", TenantID: "t1", CustomerID: "cus_1",
				Status: domain.InvoicePaid, PaymentStatus: domain.PaymentSucceeded,
				Currency: "USD", SubtotalCents: 10000, TaxAmountCents: 1800,
				TotalAmountCents: 11800, AmountPaidCents: 11800,
				StripePaymentIntentID: "pi_1",
				TaxTransactionID:      "tx_upstream_1",
			},
		},
	}
	refunder := &fakeRefunder{}
	rev := &fakeTaxReverser{returnID: "tx_reversal_1"}
	svc := NewService(store, invoices, refunder, &fakeCreditGranter{})
	svc.SetTaxReverser(rev)

	cn, err := svc.CreateRefund(context.Background(), "t1", RefundInput{
		InvoiceID: "inv_paid", Reason: "duplicate",
	})
	if err != nil {
		t.Fatalf("CreateRefund: %v", err)
	}
	if len(rev.calls) != 1 {
		t.Fatalf("reverse calls: got %d, want 1", len(rev.calls))
	}
	call := rev.calls[0]
	if call.OriginalTransactionID != "tx_upstream_1" {
		t.Errorf("OriginalTransactionID: got %q, want tx_upstream_1", call.OriginalTransactionID)
	}
	if call.Mode != tax.ReversalModePartial {
		t.Errorf("Mode: got %q, want partial", call.Mode)
	}
	if call.GrossAmountCents != 11800 {
		t.Errorf("GrossAmountCents: got %d, want 11800", call.GrossAmountCents)
	}
	if call.CreditNoteID == "" {
		t.Error("CreditNoteID should be set for idempotency")
	}
	// Verify reversal id got persisted.
	stored, err := store.Get(context.Background(), "t1", cn.ID)
	if err != nil {
		t.Fatalf("Get stored CN: %v", err)
	}
	if stored.TaxTransactionID != "tx_reversal_1" {
		t.Errorf("stored TaxTransactionID: got %q, want tx_reversal_1", stored.TaxTransactionID)
	}
}

// TestIssue_NoReversalWhenInvoiceHasNoTransactionID covers tenants on
// manual/none providers (or legacy invoices from before the commit
// column existed). The reverser must not be called — invoking it with
// an empty OriginalTransactionID would produce a provider error.
func TestIssue_NoReversalWhenInvoiceHasNoTransactionID(t *testing.T) {
	t.Parallel()
	store := newMemStore()
	invoices := &memInvoiceReader{
		invoices: map[string]domain.Invoice{
			"inv_manual": {
				ID: "inv_manual", TenantID: "t1", CustomerID: "cus_1",
				Status: domain.InvoicePaid, PaymentStatus: domain.PaymentSucceeded,
				Currency: "USD", SubtotalCents: 10000, TaxAmountCents: 1800,
				TotalAmountCents: 11800, AmountPaidCents: 11800,
				StripePaymentIntentID: "pi_2",
				// TaxTransactionID deliberately empty.
			},
		},
	}
	refunder := &fakeRefunder{}
	rev := &fakeTaxReverser{}
	svc := NewService(store, invoices, refunder, &fakeCreditGranter{})
	svc.SetTaxReverser(rev)

	_, err := svc.CreateRefund(context.Background(), "t1", RefundInput{
		InvoiceID: "inv_manual", Reason: "duplicate",
	})
	if err != nil {
		t.Fatalf("CreateRefund: %v", err)
	}
	if len(rev.calls) != 0 {
		t.Errorf("reverse calls: got %d, want 0 (no upstream state)", len(rev.calls))
	}
}

// TestIssue_NoReverserWiredDoesNotCall covers the narrow-test and
// no-provider-configured paths. Without SetTaxReverser the CN issues
// normally without crashing.
func TestIssue_NoReverserWiredDoesNotCall(t *testing.T) {
	t.Parallel()
	store := newMemStore()
	invoices := &memInvoiceReader{
		invoices: map[string]domain.Invoice{
			"inv_paid": {
				ID: "inv_paid", TenantID: "t1", CustomerID: "cus_1",
				Status: domain.InvoicePaid, PaymentStatus: domain.PaymentSucceeded,
				Currency: "USD", TotalAmountCents: 10000, AmountPaidCents: 10000,
				StripePaymentIntentID: "pi_3",
				TaxTransactionID:      "tx_upstream_x",
			},
		},
	}
	refunder := &fakeRefunder{}
	svc := NewService(store, invoices, refunder, &fakeCreditGranter{})

	cn, err := svc.CreateRefund(context.Background(), "t1", RefundInput{
		InvoiceID: "inv_paid", Reason: "duplicate",
	})
	if err != nil {
		t.Fatalf("CreateRefund: %v", err)
	}
	if cn.Status != domain.CreditNoteIssued {
		t.Errorf("status: got %q, want issued", cn.Status)
	}
	if cn.TaxTransactionID != "" {
		t.Errorf("TaxTransactionID: got %q, want empty (no reverser)", cn.TaxTransactionID)
	}
}

// TestIssue_ReversalFailureStillIssuesCN — parity with the Stripe-refund
// failure policy: the CN is an accounting document, so a provider error
// on the tax reversal is logged (warn) but the CN still transitions to
// issued. Operators reconcile manually.
func TestIssue_ReversalFailureStillIssuesCN(t *testing.T) {
	t.Parallel()
	store := newMemStore()
	invoices := &memInvoiceReader{
		invoices: map[string]domain.Invoice{
			"inv_paid": {
				ID: "inv_paid", TenantID: "t1", CustomerID: "cus_1",
				Status: domain.InvoicePaid, PaymentStatus: domain.PaymentSucceeded,
				Currency: "USD", SubtotalCents: 10000, TaxAmountCents: 1800,
				TotalAmountCents: 11800, AmountPaidCents: 11800,
				StripePaymentIntentID: "pi_4",
				TaxTransactionID:      "tx_upstream_2",
			},
		},
	}
	refunder := &fakeRefunder{}
	rev := &fakeTaxReverser{failWith: errors.New("stripe: api error")}
	svc := NewService(store, invoices, refunder, &fakeCreditGranter{})
	svc.SetTaxReverser(rev)

	cn, err := svc.CreateRefund(context.Background(), "t1", RefundInput{
		InvoiceID: "inv_paid", Reason: "fraudulent",
	})
	if err != nil {
		t.Fatalf("CreateRefund: %v", err)
	}
	if cn.Status != domain.CreditNoteIssued {
		t.Errorf("status: got %q, want issued (CN issues even on reversal failure)", cn.Status)
	}
	if cn.TaxTransactionID != "" {
		t.Errorf("TaxTransactionID: got %q, want empty on failure", cn.TaxTransactionID)
	}
	if len(rev.calls) != 1 {
		t.Errorf("reverse calls: got %d, want 1", len(rev.calls))
	}
}

// fakeCreditGranter satisfies the CreditGranter optional dep so
// CreateRefund doesn't blow up when refund_type is credit. The refund
// happy path doesn't call Grant, but wiring one keeps the service's
// optional-dep surface exercised.
type fakeCreditGranter struct {
	calls []CreditGrantInput
}

func (f *fakeCreditGranter) Grant(_ context.Context, _ string, in CreditGrantInput) error {
	f.calls = append(f.calls, in)
	return nil
}
