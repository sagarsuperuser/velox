package creditnote

import (
	"context"
	"errors"
	"testing"

	"github.com/sagarsuperuser/velox/internal/domain"
)

type fakeCouponVoider struct {
	calls   []fakeVoidCall
	n       int
	failErr error
}

type fakeVoidCall struct {
	tenantID  string
	invoiceID string
}

func (f *fakeCouponVoider) VoidRedemptionsForInvoice(_ context.Context, tenantID, invoiceID string) (int, error) {
	f.calls = append(f.calls, fakeVoidCall{tenantID: tenantID, invoiceID: invoiceID})
	if f.failErr != nil {
		return 0, f.failErr
	}
	return f.n, nil
}

// setupCouponVoidSvc wires a Service with an unpaid invoice (TotalAmountCents
// 10000) and a fake coupon voider ready to record calls.
func setupCouponVoidSvc(t *testing.T) (*Service, *memStore, *memInvoiceReader, *fakeCouponVoider) {
	t.Helper()
	store := newMemStore()
	invoices := &memInvoiceReader{
		invoices: map[string]domain.Invoice{
			"inv_1": {
				ID: "inv_1", TenantID: "t1", CustomerID: "cus_1",
				Status: domain.InvoiceFinalized, Currency: "USD",
				TotalAmountCents: 10000, AmountDueCents: 10000,
			},
		},
	}
	voider := &fakeCouponVoider{n: 2}
	svc := NewService(store, invoices, nil)
	svc.SetCouponRedemptionVoider(voider)
	return svc, store, invoices, voider
}

// draftCN is a small helper to create a draft CN on inv_1 with a given total.
func draftCN(t *testing.T, svc *Service, totalCents int64) domain.CreditNote {
	t.Helper()
	cn, err := svc.Create(context.Background(), "t1", CreateInput{
		InvoiceID: "inv_1",
		Reason:    "Coupon void test",
		Lines: []CreditLineInput{
			{Description: "adj", Quantity: 1, UnitAmountCents: totalCents},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return cn
}

func TestIssue_FullCreditNoteVoidsCouponRedemptions(t *testing.T) {
	t.Parallel()
	svc, _, _, voider := setupCouponVoidSvc(t)

	cn := draftCN(t, svc, 10000) // matches invoice total
	if _, err := svc.Issue(context.Background(), "t1", cn.ID); err != nil {
		t.Fatalf("Issue: %v", err)
	}

	if len(voider.calls) != 1 {
		t.Fatalf("voider calls: got %d, want 1", len(voider.calls))
	}
	if voider.calls[0].tenantID != "t1" || voider.calls[0].invoiceID != "inv_1" {
		t.Errorf("voider call args: got %+v, want {t1 inv_1}", voider.calls[0])
	}
}

func TestIssue_OverCreditTriggersVoid(t *testing.T) {
	t.Parallel()
	// Defensive: CN.TotalCents > inv.TotalAmountCents shouldn't happen under
	// Create's cap, but the Issue-time threshold is >= so over-credit still
	// triggers reversal rather than silently leaving redemptions.
	svc, store, _, voider := setupCouponVoidSvc(t)

	cn := draftCN(t, svc, 5000)
	// Mutate the stored CN total to simulate an over-credit.
	stored := store.notes[cn.ID]
	stored.TotalCents = 12000
	store.notes[cn.ID] = stored

	if _, err := svc.Issue(context.Background(), "t1", cn.ID); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if len(voider.calls) != 1 {
		t.Fatalf("voider calls: got %d, want 1 (over-credit should trigger)", len(voider.calls))
	}
}

func TestIssue_PartialCreditNoteSkipsCouponVoid(t *testing.T) {
	t.Parallel()
	svc, _, _, voider := setupCouponVoidSvc(t)

	cn := draftCN(t, svc, 3000) // partial — less than invoice total
	if _, err := svc.Issue(context.Background(), "t1", cn.ID); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if len(voider.calls) != 0 {
		t.Errorf("voider calls: got %d, want 0 (partial CN shouldn't void redemptions)", len(voider.calls))
	}
}

func TestIssue_VoiderErrorDoesNotBlockIssuance(t *testing.T) {
	t.Parallel()
	// Coupon reversal is best-effort: if the voider errors, the CN must
	// still issue — the operator resolves the coupon state manually.
	svc, _, _, voider := setupCouponVoidSvc(t)
	voider.failErr = errors.New("db unreachable")

	cn := draftCN(t, svc, 10000)
	issued, err := svc.Issue(context.Background(), "t1", cn.ID)
	if err != nil {
		t.Fatalf("Issue should not surface voider error: %v", err)
	}
	if issued.Status != domain.CreditNoteIssued {
		t.Errorf("status: got %q, want issued", issued.Status)
	}
	if len(voider.calls) != 1 {
		t.Errorf("voider still called once, got %d", len(voider.calls))
	}
}

func TestIssue_VoiderNotWired_NoOp(t *testing.T) {
	t.Parallel()
	// When the router doesn't call SetCouponRedemptionVoider (narrow tests,
	// tenants without coupons), Issue must still work.
	store := newMemStore()
	invoices := &memInvoiceReader{
		invoices: map[string]domain.Invoice{
			"inv_1": {
				ID: "inv_1", TenantID: "t1", CustomerID: "cus_1",
				Status: domain.InvoiceFinalized, Currency: "USD",
				TotalAmountCents: 10000, AmountDueCents: 10000,
			},
		},
	}
	svc := NewService(store, invoices, nil)

	cn := draftCN(t, svc, 10000)
	issued, err := svc.Issue(context.Background(), "t1", cn.ID)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if issued.Status != domain.CreditNoteIssued {
		t.Errorf("status: got %q, want issued", issued.Status)
	}
}

func TestIssue_ZeroInvoiceTotalSkipsVoid(t *testing.T) {
	t.Parallel()
	// Guard: inv.TotalAmountCents > 0 prevents a zero-total invoice from
	// spuriously satisfying cn.TotalCents >= inv.TotalAmountCents (both 0).
	svc, store, invoices, voider := setupCouponVoidSvc(t)

	cn := draftCN(t, svc, 5000)
	// Mutate both the CN total and invoice total to 0 to exercise the guard.
	stored := store.notes[cn.ID]
	stored.TotalCents = 0
	store.notes[cn.ID] = stored
	inv := invoices.invoices["inv_1"]
	inv.TotalAmountCents = 0
	invoices.invoices["inv_1"] = inv

	if _, err := svc.Issue(context.Background(), "t1", cn.ID); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if len(voider.calls) != 0 {
		t.Errorf("voider calls: got %d, want 0 for zero-total invoice", len(voider.calls))
	}
}
