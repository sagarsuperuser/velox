package billing

import (
	"context"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/platform/clock"
)

// trialEngineFixture: a due, trial-elapsed sub on the standard mock plan.
func trialEngineFixture(cancelAtPeriodEnd bool) (*Engine, *thresholdMockSubs, *mockInvoices, time.Time) {
	engine, subs, invoices := setupThresholdEngine(nil, 1000)
	trialEnd := time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC)
	engine.clock = clock.NewFake(time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)) // due + trial over
	sub := subs.subs["sub_1"]
	sub.Status = domain.SubscriptionTrialing
	sub.TrialEndAt = &trialEnd
	sub.CancelAtPeriodEnd = cancelAtPeriodEnd
	subs.subs["sub_1"] = sub
	return engine, subs, invoices, trialEnd
}

// TestEngineTrialBranch_DueCancel_NoInvoiceNoAdvance is the ADR-069 engine
// regression: the fourth activation writer (the cycle scan racing the trial
// scan) must route a due cancel schedule to the FREE cancel — zero invoices,
// zero cycle advance, sub canceled at trial_end. Pre-fix the branch
// activated and fell through to a full cycle-close bill. Mutation seam:
// drop the engine's pre-route (or the ErrTrialCancelDue routing) and this
// fails by billing a canceled customer.
func TestEngineTrialBranch_DueCancel_NoInvoiceNoAdvance(t *testing.T) {
	engine, subs, invoices, trialEnd := trialEngineFixture(true)

	generated, failures := engine.RunCycleForTenant(context.Background(), "t1", 50)
	if len(failures) != 0 {
		t.Fatalf("failures: %v", failures)
	}
	if generated != 0 {
		t.Fatalf("generated = %d, want 0 (trials with a due cancel are FREE)", generated)
	}
	if len(invoices.invoices) != 0 {
		t.Fatalf("invoices = %d, want 0 — the engine billed a customer who canceled", len(invoices.invoices))
	}
	out := subs.subs["sub_1"]
	if out.Status != domain.SubscriptionCanceled {
		t.Fatalf("status = %s, want canceled", out.Status)
	}
	if out.CanceledAt == nil || !out.CanceledAt.Equal(trialEnd) {
		t.Fatalf("canceled_at = %v, want trial_end %v", out.CanceledAt, trialEnd)
	}
	if subs.cycleUpdated["sub_1"] {
		t.Fatal("cycle advanced on a trial-end cancel — the watermark must not move on a terminal sub")
	}
}

// TestEngineTrialBranch_NoSchedule_ActivatesAndBills is the control: without
// a schedule the trial-elapsed sub activates (day-1 bill riding the flip)
// and the cycle proceeds.
func TestEngineTrialBranch_NoSchedule_ActivatesAndBills(t *testing.T) {
	engine, subs, _, _ := trialEngineFixture(false)

	_, failures := engine.RunCycleForTenant(context.Background(), "t1", 50)
	if len(failures) != 0 {
		t.Fatalf("failures: %v", failures)
	}
	if subs.subs["sub_1"].Status != domain.SubscriptionActive {
		t.Fatalf("status = %s, want active", subs.subs["sub_1"].Status)
	}
}

// TestBillFinalOnImmediateCancel_TrialWriteOff locks the decided ADR-069
// semantics for the post-trial-lag window: an immediate cancel of a
// never-activated trial (trial elapsed, activation scan lagging,
// canceled_at inside the first paid period) emits NO final invoice — trials
// are free. Control: an ACTIVATED sub canceled in the same window still
// bills. Mutation seam: drop the activated_at==nil guard.
func TestBillFinalOnImmediateCancel_TrialWriteOff(t *testing.T) {
	engine, subs, invoices, trialEnd := trialEngineFixture(false)
	sub := subs.subs["sub_1"]
	sub.Status = domain.SubscriptionCanceled
	// Post-trial-lag shape: canceled after trial_end (= period start for the
	// first paid period), never activated.
	periodStart := trialEnd
	periodEnd := trialEnd.Add(30 * 24 * time.Hour)
	canceledAt := trialEnd.Add(5 * 24 * time.Hour)
	sub.CurrentBillingPeriodStart = &periodStart
	sub.CurrentBillingPeriodEnd = &periodEnd
	sub.CanceledAt = &canceledAt
	sub.ActivatedAt = nil
	subs.subs["sub_1"] = sub

	inv, err := engine.BillFinalOnImmediateCancel(context.Background(), sub)
	if err != nil {
		t.Fatalf("bill final: %v", err)
	}
	if inv.ID != "" || len(invoices.invoices) != 0 {
		t.Fatalf("never-activated trial billed a final invoice (%q, %d rows) — trials are free", inv.ID, len(invoices.invoices))
	}

	// Control: the same window on an ACTIVATED sub bills normally.
	activatedAt := trialEnd
	sub.ActivatedAt = &activatedAt
	subs.subs["sub_1"] = sub
	inv2, err := engine.BillFinalOnImmediateCancel(context.Background(), sub)
	if err != nil {
		t.Fatalf("bill final (activated): %v", err)
	}
	if inv2.ID == "" {
		t.Fatal("activated sub's mid-period cancel must still bill the final invoice")
	}
}
