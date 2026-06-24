package subscription

import (
	"context"
	"errors"
	"testing"
)

// seedActiveSub creates an active in_advance-style sub for cancel wiring tests.
func seedActiveSub(t *testing.T, svc *Service, fb *fakeBiller) string {
	t.Helper()
	created, err := svc.Create(context.Background(), "t1", CreateInput{
		Code: "sub-cancel-wire", DisplayName: "Wire", CustomerID: "cus_1",
		Items: []CreateItemInput{{PlanID: "pln_1"}}, StartNow: true,
	})
	if err != nil {
		t.Fatalf("seed create: %v", err)
	}
	// Neutralize the create-path counters so the cancel assertions are clean.
	fb.createTxCalls = 0
	fb.draftTxCalls = 0
	return created.ID
}

// TestCancel_DraftPath_IssuesDraftsAndSkipsBillOnCancel: when BillOnCancelDraftsTx
// reports handled (all-paid funding → drafts created in the cancel tx), Cancel
// issues those drafts post-commit and reports their credit — and does NOT fall
// through to the post-commit BillOnCancel.
func TestCancel_DraftPath_IssuesDraftsAndSkipsBillOnCancel(t *testing.T) {
	ctx := context.Background()
	svc := NewService(newMemStore(), nil)
	fb := &fakeBiller{}
	svc.SetBiller(fb)
	id := seedActiveSub(t, svc, fb)

	fb.draftHandled = true
	fb.draftIDs = []string{"cn_draft_1"}
	fb.draftCredit = 5000

	_, credit, err := svc.Cancel(ctx, "t1", id)
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if credit != 5000 {
		t.Errorf("proration credit = %d, want 5000 (the draft gross)", credit)
	}
	if fb.draftTxCalls != 1 {
		t.Errorf("BillOnCancelDraftsTx calls = %d, want 1", fb.draftTxCalls)
	}
	if fb.issueDraftCalls != 1 || len(fb.issuedDraftIDs) != 1 || fb.issuedDraftIDs[0] != "cn_draft_1" {
		t.Errorf("IssueCancelDrafts not invoked with the drafts; calls=%d ids=%v", fb.issueDraftCalls, fb.issuedDraftIDs)
	}
	if fb.cancelCalls != 0 {
		t.Errorf("BillOnCancel (fallback) must NOT run on the atomic path; calls=%d", fb.cancelCalls)
	}
}

// TestCancel_DeclinedDraftPath_FallsBackToBillOnCancel: when the in-tx path
// declines (any unpaid funding source), Cancel falls back to the post-commit
// BillOnCancel and does not issue drafts.
func TestCancel_DeclinedDraftPath_FallsBackToBillOnCancel(t *testing.T) {
	ctx := context.Background()
	svc := NewService(newMemStore(), nil)
	fb := &fakeBiller{}
	svc.SetBiller(fb)
	id := seedActiveSub(t, svc, fb)

	fb.draftHandled = false // declined → fallback
	fb.cancelCreditCents = 3000

	_, credit, err := svc.Cancel(ctx, "t1", id)
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if credit != 3000 {
		t.Errorf("proration credit = %d, want 3000 (BillOnCancel fallback)", credit)
	}
	if fb.cancelCalls != 1 {
		t.Errorf("BillOnCancel fallback must run; calls=%d", fb.cancelCalls)
	}
	if fb.issueDraftCalls != 0 {
		t.Errorf("IssueCancelDrafts must NOT run on the fallback path; calls=%d", fb.issueDraftCalls)
	}
}

// TestCancel_DraftError_FailsCancel: a draft-create failure inside the cancel tx
// surfaces as a Cancel error (the billFn returns it → CancelAtomicWithBill rolls
// back), never a canceled sub with a silently-lost credit.
func TestCancel_DraftError_FailsCancel(t *testing.T) {
	ctx := context.Background()
	svc := NewService(newMemStore(), nil)
	fb := &fakeBiller{}
	svc.SetBiller(fb)
	id := seedActiveSub(t, svc, fb)

	fb.draftTxErr = errors.New("draft insert failed")

	if _, _, err := svc.Cancel(ctx, "t1", id); err == nil {
		t.Fatal("a draft-create failure must fail the cancel (so the tx rolls back)")
	}
}
