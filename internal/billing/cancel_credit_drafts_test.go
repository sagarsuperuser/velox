package billing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// canceledInAdvanceSub builds a mid-period-canceled in_advance sub funded by the
// given invoices, for the cancel-credit-draft tests. Period Jun1→Jul1, canceled
// Jun21 → 10/30 unused.
func canceledInAdvanceSub(invoices []domain.Invoice, lines []domain.InvoiceLineItem) (domain.Subscription, *mockInvoices, *mockPricing) {
	periodStart := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	cancelAt := time.Date(2026, 6, 21, 0, 0, 0, 0, time.UTC)
	pricing := &mockPricing{plans: map[string]domain.Plan{
		"pln_base": {ID: "pln_base", Currency: "USD", BillingInterval: domain.BillingMonthly,
			BaseAmountCents: 15000, BaseBillTiming: domain.BillInAdvance},
	}}
	sub := domain.Subscription{
		ID: "sub_1", TenantID: "t1", CustomerID: "cus_1", Code: "sub",
		Status:                    domain.SubscriptionCanceled,
		Items:                     []domain.SubscriptionItem{{PlanID: "pln_base", Quantity: 1}},
		CurrentBillingPeriodStart: &periodStart, CurrentBillingPeriodEnd: &periodEnd,
		CanceledAt: &cancelAt,
	}
	return sub, &mockInvoices{invoices: invoices, lineItems: lines}, pricing
}

func paidBaseInvoice() (domain.Invoice, domain.InvoiceLineItem) {
	periodStart := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	inv := domain.Invoice{
		ID: "inv_base", TenantID: "t1", SubscriptionID: "sub_1",
		Status: domain.InvoiceFinalized, PaymentStatus: domain.PaymentSucceeded,
		SubtotalCents: 15000, TotalAmountCents: 15000,
		BillingPeriodStart: periodStart, BillingPeriodEnd: periodEnd,
	}
	line := domain.InvoiceLineItem{ID: "ili_base", InvoiceID: "inv_base", LineType: domain.LineTypeBaseFee, BillingPeriodStart: &periodStart}
	return inv, line
}

func wireCancelEngine(inv *mockInvoices, pricing *mockPricing, adjuster *fakeCreditNoteAdjuster) *Engine {
	e := wireBaseTax(NewEngine(&mockSubs{}, &mockUsage{}, pricing, inv, nil, &mockSettings{}, nil, nil, billingTestClock()))
	e.SetCreditGranter(&fakeCreditGranter{})
	e.SetCreditNoteAdjuster(adjuster)
	e.SetInvoiceVoider(&fakeInvoiceVoider{})
	return e
}

// TestBillOnCancelDraftsTx_AllPaid_CreatesDraftsNotIssue: when the funding set is
// all-paid, the atomic path creates issue_pending DRAFTS (CreateAdjustmentDraftTx),
// NOT an immediate CreateAndIssueAdjustment, and reports handled + the gross owed.
func TestBillOnCancelDraftsTx_AllPaid_CreatesDraftsNotIssue(t *testing.T) {
	baseInv, baseLine := paidBaseInvoice()
	sub, inv, pricing := canceledInAdvanceSub([]domain.Invoice{baseInv}, []domain.InvoiceLineItem{baseLine})
	adjuster := &fakeCreditNoteAdjuster{}
	e := wireCancelEngine(inv, pricing, adjuster)

	ids, credited, handled, err := e.BillOnCancelDraftsTx(context.Background(), nil, sub)
	if err != nil {
		t.Fatalf("BillOnCancelDraftsTx: %v", err)
	}
	if !handled {
		t.Fatal("all-paid funding must be handled by the in-tx draft path")
	}
	const want = 5000 // 15000 * 10/30
	if credited != want {
		t.Errorf("credited=%d, want %d", credited, want)
	}
	if len(ids) != 1 {
		t.Fatalf("got %d draft ids, want 1: %v", len(ids), ids)
	}
	if len(adjuster.draftCall) != 1 || adjuster.draftCall[0].gross != want {
		t.Errorf("draft calls=%+v, want one of gross %d", adjuster.draftCall, want)
	}
	if len(adjuster.calls) != 0 {
		t.Errorf("must NOT CreateAndIssueAdjustment on the draft path; got %+v", adjuster.calls)
	}

	// Post-commit issue relays the drafts.
	e.IssueCancelDrafts(context.Background(), sub, ids)
	if len(adjuster.issued) != 1 || adjuster.issued[0] != ids[0] {
		t.Errorf("IssueCancelDrafts issued=%v, want %v", adjuster.issued, ids)
	}
}

// TestBillOnCancelDraftsTx_AnyUnpaid_Declines: a mixed paid+unpaid funding set
// declines the in-tx path (handled=false, no drafts) so the caller falls back to
// the post-commit BillOnCancel (PR1: unpaid relief stays post-commit).
func TestBillOnCancelDraftsTx_AnyUnpaid_Declines(t *testing.T) {
	baseInv, baseLine := paidBaseInvoice()
	changeAt := time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	upInv := domain.Invoice{
		ID: "inv_up", TenantID: "t1", SubscriptionID: "sub_1",
		Status: domain.InvoiceFinalized, PaymentStatus: domain.PaymentPending,
		SubtotalCents: 8000, TotalAmountCents: 8000, AmountDueCents: 8000,
		BillingReason: domain.BillingReasonSubscriptionUpdate, SourcePlanChangedAt: &changeAt,
		BillingPeriodStart: changeAt, BillingPeriodEnd: periodEnd,
	}
	sub, inv, pricing := canceledInAdvanceSub([]domain.Invoice{baseInv, upInv}, []domain.InvoiceLineItem{baseLine})
	adjuster := &fakeCreditNoteAdjuster{}
	e := wireCancelEngine(inv, pricing, adjuster)

	ids, credited, handled, err := e.BillOnCancelDraftsTx(context.Background(), nil, sub)
	if err != nil {
		t.Fatalf("BillOnCancelDraftsTx: %v", err)
	}
	if handled {
		t.Error("a funding set with any unpaid source must decline the in-tx path")
	}
	if len(ids) != 0 || credited != 0 || len(adjuster.draftCall) != 0 {
		t.Errorf("declined path must create no drafts; ids=%v credited=%d drafts=%+v", ids, credited, adjuster.draftCall)
	}
}

// TestBillOnCancelDraftsTx_DraftError_PropagatesForRollback: a draft-create
// failure returns an error so the caller (CancelAtomicWithBill billFn) rolls the
// cancel back — never a canceled sub with a silently-lost credit.
func TestBillOnCancelDraftsTx_DraftError_PropagatesForRollback(t *testing.T) {
	baseInv, baseLine := paidBaseInvoice()
	sub, inv, pricing := canceledInAdvanceSub([]domain.Invoice{baseInv}, []domain.InvoiceLineItem{baseLine})
	adjuster := &fakeCreditNoteAdjuster{draftErr: errors.New("draft insert failed")}
	e := wireCancelEngine(inv, pricing, adjuster)

	if _, _, _, err := e.BillOnCancelDraftsTx(context.Background(), nil, sub); err == nil {
		t.Fatal("a draft-create failure must return an error (so the cancel rolls back)")
	}
}

// TestBillOnCancelDraftsTx_MultiPaidSource_RespectsHeadroom pins the #276/#277/#278
// invariant across the allocate→draft split: cumulative draft gross never exceeds
// the funding sources' combined headroom.
func TestBillOnCancelDraftsTx_MultiPaidSource_RespectsHeadroom(t *testing.T) {
	baseInv, baseLine := paidBaseInvoice()
	changeAt := time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)
	periodStart := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	// A second PAID funding invoice (mid-period upgrade proration), fully paid.
	upInv := domain.Invoice{
		ID: "inv_up", TenantID: "t1", SubscriptionID: "sub_1",
		Status: domain.InvoiceFinalized, PaymentStatus: domain.PaymentSucceeded,
		SubtotalCents: 5000, TotalAmountCents: 5000,
		BillingReason: domain.BillingReasonSubscriptionUpdate, SourcePlanChangedAt: &changeAt,
		BillingPeriodStart: changeAt, BillingPeriodEnd: periodEnd,
	}
	_ = periodStart
	sub, inv, pricing := canceledInAdvanceSub([]domain.Invoice{baseInv, upInv}, []domain.InvoiceLineItem{baseLine})
	adjuster := &fakeCreditNoteAdjuster{}
	e := wireCancelEngine(inv, pricing, adjuster)

	_, credited, handled, err := e.BillOnCancelDraftsTx(context.Background(), nil, sub)
	if err != nil {
		t.Fatalf("BillOnCancelDraftsTx: %v", err)
	}
	if !handled {
		t.Fatal("all-paid multi-source must be handled")
	}
	var sum int64
	for _, c := range adjuster.draftCall {
		sum += c.gross
	}
	if sum != credited {
		t.Errorf("draft gross sum %d != reported credited %d", sum, credited)
	}
	// Combined headroom = base 15000 + upgrade 5000 = 20000; the owed credit is
	// well under it, and no single draft may exceed its own source's total.
	if sum > 20000 {
		t.Errorf("cumulative draft gross %d exceeds combined funding headroom 20000", sum)
	}
}
