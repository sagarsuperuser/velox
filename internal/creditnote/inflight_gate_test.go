package creditnote

import (
	"context"
	"errors"
	"testing"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
)

// TestCreate_InFlightPaymentGate pins the Part-A gate: an OPERATOR credit note
// must not reduce amount_due while the invoice's payment is in flight
// (processing/unknown) — else MarkPaid at settle records amount_paid off the
// lowered amount_due, under-sizing the refund cap (invoice/postgres.go KNOWN
// EDGE). The AUTOMATED clawback (CreateAndIssueAdjustment, ADR-050) must STILL
// proceed — it calls create() directly, bypassing the gate.
func TestCreate_InFlightPaymentGate(t *testing.T) {
	ctx := context.Background()
	line := []CreditLineInput{{Description: "adj", Quantity: 1, UnitAmountCents: 1000}}
	mk := func(id string, ps domain.InvoicePaymentStatus, st domain.InvoiceStatus) domain.Invoice {
		return domain.Invoice{
			ID: id, TenantID: "t1", CustomerID: "cus_1",
			Status: st, PaymentStatus: ps, Currency: "USD",
			TotalAmountCents: 10000, AmountDueCents: 10000,
		}
	}
	newSvc := func(inv domain.Invoice) (*Service, *memStore) {
		store := newMemStore()
		s := NewService(store, &memInvoiceReader{invoices: map[string]domain.Invoice{inv.ID: inv}}, nil)
		s.SetNumberGenerator(&fakeCNNumbers{})
		return s, store
	}

	t.Run("operator CN on a processing invoice is rejected (InvalidState→409)", func(t *testing.T) {
		s, store := newSvc(mk("inv_proc", domain.PaymentProcessing, domain.InvoiceFinalized))
		_, err := s.Create(ctx, "t1", CreateInput{InvoiceID: "inv_proc", Reason: "downgrade", Lines: line})
		if !errors.Is(err, errs.ErrInvalidState) {
			t.Fatalf("want ErrInvalidState (→409) for an in-flight payment, got %v", err)
		}
		if len(store.notes) != 0 {
			t.Errorf("no credit note should be created when gated, got %d", len(store.notes))
		}
	})

	t.Run("operator CN on an unknown-payment invoice is rejected", func(t *testing.T) {
		s, _ := newSvc(mk("inv_unk", domain.PaymentUnknown, domain.InvoiceFinalized))
		_, err := s.Create(ctx, "t1", CreateInput{InvoiceID: "inv_unk", Reason: "downgrade", Lines: line})
		if !errors.Is(err, errs.ErrInvalidState) {
			t.Errorf("ambiguous (unknown) outcome is in-flight too; want ErrInvalidState, got %v", err)
		}
	})

	t.Run("operator CN on a not-in-flight (pending) finalized invoice is allowed", func(t *testing.T) {
		s, _ := newSvc(mk("inv_pend", domain.PaymentPending, domain.InvoiceFinalized))
		if _, err := s.Create(ctx, "t1", CreateInput{InvoiceID: "inv_pend", Reason: "downgrade", Lines: line}); err != nil {
			t.Errorf("pending = no charge in flight; CN must be allowed, got %v", err)
		}
	})

	t.Run("operator CN on a PAID invoice is allowed (refund branch never reduces amount_due)", func(t *testing.T) {
		s, _ := newSvc(mk("inv_paid", domain.PaymentSucceeded, domain.InvoicePaid))
		if _, err := s.Create(ctx, "t1", CreateInput{InvoiceID: "inv_paid", Reason: "refund", Lines: line}); err != nil {
			t.Errorf("paid invoice CN must be allowed, got %v", err)
		}
	})

	t.Run("automated clawback bypasses the gate on a processing invoice (ADR-050 must proceed)", func(t *testing.T) {
		s, store := newSvc(mk("inv_auto", domain.PaymentProcessing, domain.InvoiceFinalized))
		if _, err := s.CreateAndIssueAdjustment(ctx, "t1", "inv_auto", 1000, "subscription_cancellation", "unused prebill"); err != nil {
			t.Fatalf("automated clawback must proceed on a processing source, got %v", err)
		}
		if len(store.notes) != 1 {
			t.Errorf("automated clawback should create + issue the CN, got %d notes", len(store.notes))
		}
	})
}
