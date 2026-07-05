package subscription

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/platform/clock"
)

// Bug B transplant wiring (2026-07-05): the cross-interval swap refund is
// created as issue_pending DRAFTS on the swap tx (BillOnPlanSwapDraftsTx),
// issued post-commit by FinalizeCrossIntervalSwap, with the legacy
// post-commit BillOnPlanSwapImmediate demoted to the declined-path fallback.
// Pre-fix the whole refund ran post-commit: a crash between the swap commit
// and the refund lost the customer's unused prepayment for good (retry 400s
// on the same-plan guard; no reconciler re-derives a missed swap refund).

func newSwapWiringSvc(t *testing.T, fb *fakeBiller) (*Service, domain.Subscription) {
	t.Helper()
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	svc := NewService(newMemStore(), clock.NewFake(now))
	svc.SetPlanReader(&fakePlanReader{plans: map[string]domain.Plan{
		"p_yearly_adv":  {ID: "p_yearly_adv", BillingInterval: domain.BillingYearly, BaseBillTiming: domain.BillInAdvance, BaseAmountCents: 120000},
		"p_monthly_adv": {ID: "p_monthly_adv", BillingInterval: domain.BillingMonthly, BaseBillTiming: domain.BillInAdvance, BaseAmountCents: 12000},
	}})
	svc.SetBiller(fb)
	savedErr, savedOK := fb.createTxErr, fb.createTxOK
	fb.createTxErr, fb.createTxOK = nil, true
	sub, err := svc.Create(context.Background(), "t1", CreateInput{
		Code: "s-swapwire", DisplayName: "n", CustomerID: "c",
		Items:       []CreateItemInput{{PlanID: "p_yearly_adv"}},
		BillingTime: domain.BillingTimeAnniversary,
		StartNow:    true,
	})
	if err != nil {
		t.Fatalf("seed create: %v", err)
	}
	fb.createTxErr, fb.createTxOK = savedErr, savedOK
	fb.createTxCalls, fb.planSwapCalls, fb.swapDraftTxCalls = 0, 0, 0
	return svc, sub
}

func TestCrossIntervalSwap_DraftPath_IssuesDraftsAndSkipsImmediate(t *testing.T) {
	ctx := context.Background()
	fb := &fakeBiller{createTxOK: true, swapDraftHandled: true, swapDraftIDs: []string{"cn_swap_1"}}
	svc, sub := newSwapWiringSvc(t, fb)

	res, err := svc.UpdateItemTx(ctx, nil, "t1", sub.ID, sub.Items[0].ID, UpdateItemInput{
		NewPlanID: "p_monthly_adv", Immediate: true,
	})
	if err != nil {
		t.Fatalf("UpdateItemTx: %v", err)
	}
	if fb.swapDraftTxCalls != 1 {
		t.Errorf("BillOnPlanSwapDraftsTx calls = %d, want 1 (refund drafts must ride the swap tx)", fb.swapDraftTxCalls)
	}
	if !res.swapRefundHandled || len(res.swapRefundDraftIDs) != 1 {
		t.Fatalf("result must carry the in-tx drafts: handled=%v ids=%v", res.swapRefundHandled, res.swapRefundDraftIDs)
	}

	svc.FinalizeCrossIntervalSwap(ctx, "t1", sub, res)
	if fb.issueSwapDraftCalls != 1 || len(fb.issuedSwapDraftIDs) != 1 || fb.issuedSwapDraftIDs[0] != "cn_swap_1" {
		t.Errorf("IssueSwapDrafts not invoked with the drafts; calls=%d ids=%v", fb.issueSwapDraftCalls, fb.issuedSwapDraftIDs)
	}
	if fb.planSwapCalls != 0 {
		t.Errorf("BillOnPlanSwapImmediate (fallback) must NOT run when the in-tx half handled the refund; calls=%d — running both double-credits", fb.planSwapCalls)
	}
}

func TestCrossIntervalSwap_DeclinedDraftPath_FallsBackToImmediate(t *testing.T) {
	ctx := context.Background()
	fb := &fakeBiller{createTxOK: true, swapDraftHandled: false} // declined (e.g. unpaid funding)
	svc, sub := newSwapWiringSvc(t, fb)

	res, err := svc.UpdateItemTx(ctx, nil, "t1", sub.ID, sub.Items[0].ID, UpdateItemInput{
		NewPlanID: "p_monthly_adv", Immediate: true,
	})
	if err != nil {
		t.Fatalf("UpdateItemTx: %v", err)
	}
	svc.FinalizeCrossIntervalSwap(ctx, "t1", sub, res)
	if fb.planSwapCalls != 1 {
		t.Errorf("BillOnPlanSwapImmediate fallback must run when the in-tx half declined; calls=%d", fb.planSwapCalls)
	}
	if fb.issueSwapDraftCalls != 0 {
		t.Errorf("IssueSwapDrafts must NOT run on the declined path; calls=%d", fb.issueSwapDraftCalls)
	}
}

func TestCrossIntervalSwap_DraftError_FailsSwap(t *testing.T) {
	ctx := context.Background()
	fb := &fakeBiller{createTxOK: true, swapDraftTxErr: errors.New("draft insert failed")}
	svc, sub := newSwapWiringSvc(t, fb)

	if _, err := svc.UpdateItemTx(ctx, nil, "t1", sub.ID, sub.Items[0].ID, UpdateItemInput{
		NewPlanID: "p_monthly_adv", Immediate: true,
	}); err == nil {
		t.Fatal("a draft-create failure must fail the swap (tx rolls back — never a swapped sub with a silently-lost refund)")
	}
	// Drafts run FIRST on the tx, so the plan write must not have happened.
	after, err := svc.store.Get(ctx, "t1", sub.ID)
	if err != nil {
		t.Fatalf("re-read sub: %v", err)
	}
	if after.Items[0].PlanID != "p_yearly_adv" {
		t.Errorf("plan must be unchanged after a failed swap; got %q", after.Items[0].PlanID)
	}
}
