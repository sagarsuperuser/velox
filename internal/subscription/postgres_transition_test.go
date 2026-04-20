package subscription_test

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/customer"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
	"github.com/sagarsuperuser/velox/internal/pricing"
	"github.com/sagarsuperuser/velox/internal/subscription"
	"github.com/sagarsuperuser/velox/internal/testutil"
)

// TestCancelAtomic_OneWinnerUnderContention is the regression test for COR-4c:
// Cancel previously read the subscription in one tx and wrote the updated
// status back in a separate tx, so N goroutines could each observe status
// "active", each pass the transition check, and each issue an UPDATE — the
// final write wins but every caller returns success. Callers that then acted
// on that "success" (e.g. credit-refund, email dispatch) would fire N times.
// The atomic implementation scopes the transition check to the UPDATE WHERE
// clause, so exactly one caller sees a row returned and the rest correctly
// fail with a stale-status error.
func TestCancelAtomic_OneWinnerUnderContention(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx := context.Background()

	store := subscription.NewPostgresStore(db)
	svc := subscription.NewService(store, nil)
	tenantID := testutil.CreateTestTenant(t, db, "Sub Cancel Race")
	subID := seedActiveSubscription(t, db, tenantID, "cus_cancel_race", "plan_cancel_race", "sub-cancel-race")

	const goroutines = 20
	var (
		wg        sync.WaitGroup
		successes atomic.Int64
	)

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.Cancel(ctx, tenantID, subID)
			if err == nil {
				successes.Add(1)
				return
			}
			// Every non-winner must see the canceled status in the error —
			// any other error (deadlock, tenant-scope, FK) is a bug.
			if !strings.Contains(err.Error(), "can only cancel active or paused") {
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := successes.Load(); got != 1 {
		t.Fatalf("expected exactly 1 successful cancel, got %d", got)
	}

	final, err := svc.Get(ctx, tenantID, subID)
	if err != nil {
		t.Fatalf("get final sub: %v", err)
	}
	if final.Status != domain.SubscriptionCanceled {
		t.Fatalf("final status = %s, want canceled", final.Status)
	}
	if final.CanceledAt == nil {
		t.Fatal("canceled_at must be set after successful cancel")
	}
}

// TestPauseAtomic_OneWinnerUnderContention pins the same race for Pause:
// under contention, exactly one caller wins and the rest see the current
// (now "paused") status in a conflict error rather than silently succeeding.
func TestPauseAtomic_OneWinnerUnderContention(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx := context.Background()

	store := subscription.NewPostgresStore(db)
	svc := subscription.NewService(store, nil)
	tenantID := testutil.CreateTestTenant(t, db, "Sub Pause Race")
	subID := seedActiveSubscription(t, db, tenantID, "cus_pause_race", "plan_pause_race", "sub-pause-race")

	const goroutines = 20
	var (
		wg        sync.WaitGroup
		successes atomic.Int64
	)

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.Pause(ctx, tenantID, subID)
			if err == nil {
				successes.Add(1)
				return
			}
			if !strings.Contains(err.Error(), "can only pause active") {
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := successes.Load(); got != 1 {
		t.Fatalf("expected exactly 1 successful pause, got %d", got)
	}

	final, err := svc.Get(ctx, tenantID, subID)
	if err != nil {
		t.Fatalf("get final sub: %v", err)
	}
	if final.Status != domain.SubscriptionPaused {
		t.Fatalf("final status = %s, want paused", final.Status)
	}
}

// TestTransitionAtomic_NotFoundVsWrongState verifies the two-bucket error
// contract: unknown IDs return ErrNotFound (HTTP 404 upstream), while
// known IDs in a disallowed state return a conflict message with the
// current status so operators can debug without re-fetching.
func TestTransitionAtomic_NotFoundVsWrongState(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx := context.Background()

	store := subscription.NewPostgresStore(db)
	svc := subscription.NewService(store, nil)
	tenantID := testutil.CreateTestTenant(t, db, "Sub Transitions")
	subID := seedActiveSubscription(t, db, tenantID, "cus_trans", "plan_trans", "sub-trans")

	// Resume an active subscription — wrong source state.
	_, err := svc.Resume(ctx, tenantID, subID)
	if err == nil {
		t.Fatal("expected error resuming active subscription")
	}
	if !strings.Contains(err.Error(), "can only resume paused") {
		t.Errorf("expected paused-state error, got: %v", err)
	}
	if !strings.Contains(err.Error(), string(domain.SubscriptionActive)) {
		t.Errorf("error should include current status, got: %v", err)
	}

	// Cancel a nonexistent subscription — must be ErrNotFound, not a
	// status-mismatch message that would leak the schema to callers.
	_, err = svc.Cancel(ctx, tenantID, "vlx_sub_does_not_exist")
	if err == nil {
		t.Fatal("expected ErrNotFound for unknown id")
	}
	if strings.Contains(err.Error(), "can only cancel") {
		t.Errorf("expected not-found, got wrong-status error: %v", err)
	}
}

// TestApplyItemPlanImmediately_RaceConverges pins the store-level contract
// for concurrent immediate plan swaps: N goroutines each swap the same item
// to the same target plan, Postgres serializes the row-level UPDATEs, and
// the final row must reflect exactly that target — never a half-applied
// state, never a revert to the old plan, and never a bubbled serialization
// error. This is the foundation proration dedup rests on: without a
// deterministic swap under contention, the dedup key itself is a moving
// target.
//
// The realistic trigger is a user double-clicking "Change plan" or two
// browser tabs racing the same mutation. The assertion that every caller
// returned without error matters here — any bubbled 500 would surface as a
// phantom failure even though the change landed.
func TestApplyItemPlanImmediately_RaceConverges(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx := context.Background()

	store := subscription.NewPostgresStore(db)
	tenantID := testutil.CreateTestTenant(t, db, "Plan Change Race")

	cust, err := customer.NewPostgresStore(db).Create(ctx, tenantID, domain.Customer{
		ExternalID: "cus_plan_race", DisplayName: "Plan Race",
	})
	if err != nil {
		t.Fatalf("create customer: %v", err)
	}

	pricingStore := pricing.NewPostgresStore(db)
	planA, err := pricingStore.CreatePlan(ctx, tenantID, domain.Plan{
		Code: "plan-a-race", Name: "Plan A", Currency: "USD",
		BillingInterval: domain.BillingMonthly, Status: domain.PlanActive,
	})
	if err != nil {
		t.Fatalf("create plan A: %v", err)
	}
	planB, err := pricingStore.CreatePlan(ctx, tenantID, domain.Plan{
		Code: "plan-b-race", Name: "Plan B", Currency: "USD",
		BillingInterval: domain.BillingMonthly, Status: domain.PlanActive,
	})
	if err != nil {
		t.Fatalf("create plan B: %v", err)
	}

	now := time.Now().UTC()
	sub, err := store.Create(ctx, tenantID, domain.Subscription{
		Code: "sub-plan-race", DisplayName: "Plan Race Sub",
		CustomerID: cust.ID,
		Status:     domain.SubscriptionActive, BillingTime: domain.BillingTimeCalendar,
		StartedAt: &now,
		Items:     []domain.SubscriptionItem{{PlanID: planA.ID, Quantity: 1}},
	})
	if err != nil {
		t.Fatalf("create subscription: %v", err)
	}
	if len(sub.Items) != 1 {
		t.Fatalf("expected 1 item hydrated on create, got %d", len(sub.Items))
	}
	itemID := sub.Items[0].ID

	const goroutines = 20
	var (
		wg       sync.WaitGroup
		swapErrs = make(chan error, goroutines)
	)

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.ApplyItemPlanImmediately(ctx, tenantID, itemID, planB.ID, time.Now().UTC())
			if err != nil {
				swapErrs <- err
			}
		}()
	}
	wg.Wait()
	close(swapErrs)

	for err := range swapErrs {
		t.Errorf("unexpected error from ApplyItemPlanImmediately under contention: %v", err)
	}

	final, err := store.GetItem(ctx, tenantID, itemID)
	if err != nil {
		t.Fatalf("get final item: %v", err)
	}
	if final.PlanID != planB.ID {
		t.Errorf("final plan_id = %q, want %q (race did not converge to target)", final.PlanID, planB.ID)
	}
	if final.PendingPlanID != "" {
		t.Errorf("pending_plan_id not cleared after immediate swap: %q", final.PendingPlanID)
	}
	if final.PlanChangedAt == nil {
		t.Errorf("plan_changed_at not stamped after swap")
	}
}

// TestApplyItemPlanImmediately_SupersedesPendingUnderRace covers the
// immediate-vs-scheduled interleave: one goroutine schedules a future plan
// change (SetItemPendingPlan), another applies an immediate change. Since
// the immediate path clears pending_plan_id as part of its UPDATE, the
// outcome must be that the item is on the immediate's target with no
// pending remnant — regardless of which commit lands first. If this ever
// regressed, the billing engine's next-cycle ApplyDuePendingItemPlans run
// would re-swap the plan back, effectively undoing the user's immediate
// change.
func TestApplyItemPlanImmediately_SupersedesPendingUnderRace(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx := context.Background()

	store := subscription.NewPostgresStore(db)
	tenantID := testutil.CreateTestTenant(t, db, "Plan Change Supersede")

	cust, err := customer.NewPostgresStore(db).Create(ctx, tenantID, domain.Customer{
		ExternalID: "cus_supersede", DisplayName: "Supersede",
	})
	if err != nil {
		t.Fatalf("create customer: %v", err)
	}
	pricingStore := pricing.NewPostgresStore(db)
	planA, err := pricingStore.CreatePlan(ctx, tenantID, domain.Plan{
		Code: "plan-sup-a", Name: "A", Currency: "USD",
		BillingInterval: domain.BillingMonthly, Status: domain.PlanActive,
	})
	if err != nil {
		t.Fatalf("create plan A: %v", err)
	}
	planB, err := pricingStore.CreatePlan(ctx, tenantID, domain.Plan{
		Code: "plan-sup-b", Name: "B", Currency: "USD",
		BillingInterval: domain.BillingMonthly, Status: domain.PlanActive,
	})
	if err != nil {
		t.Fatalf("create plan B: %v", err)
	}
	planC, err := pricingStore.CreatePlan(ctx, tenantID, domain.Plan{
		Code: "plan-sup-c", Name: "C", Currency: "USD",
		BillingInterval: domain.BillingMonthly, Status: domain.PlanActive,
	})
	if err != nil {
		t.Fatalf("create plan C: %v", err)
	}

	now := time.Now().UTC()
	sub, err := store.Create(ctx, tenantID, domain.Subscription{
		Code: "sub-supersede", DisplayName: "Supersede Sub",
		CustomerID: cust.ID,
		Status:     domain.SubscriptionActive, BillingTime: domain.BillingTimeCalendar,
		StartedAt: &now,
		Items:     []domain.SubscriptionItem{{PlanID: planA.ID, Quantity: 1}},
	})
	if err != nil {
		t.Fatalf("create subscription: %v", err)
	}
	itemID := sub.Items[0].ID
	future := now.Add(30 * 24 * time.Hour)

	// Run scheduled + immediate concurrently. Each ordering is valid; only the
	// end state is constrained.
	const rounds = 10
	for round := 0; round < rounds; round++ {
		// Reset item to pristine state so each round exercises the race from
		// a known baseline — otherwise a prior round could leave plan=C and
		// the next round's "expect plan_id == C" assertion would pass
		// vacuously.
		if _, err := store.ApplyItemPlanImmediately(ctx, tenantID, itemID, planA.ID, now); err != nil {
			t.Fatalf("round %d: reset item: %v", round, err)
		}

		var wg sync.WaitGroup
		wg.Add(2)
		var scheduleErr, immediateErr atomic.Value
		go func() {
			defer wg.Done()
			if _, err := store.SetItemPendingPlan(ctx, tenantID, itemID, planB.ID, future); err != nil {
				scheduleErr.Store(err)
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := store.ApplyItemPlanImmediately(ctx, tenantID, itemID, planC.ID, time.Now().UTC()); err != nil {
				immediateErr.Store(err)
			}
		}()
		wg.Wait()

		if v := scheduleErr.Load(); v != nil {
			t.Errorf("round %d: schedule error: %v", round, v)
		}
		if v := immediateErr.Load(); v != nil {
			t.Errorf("round %d: immediate error: %v", round, v)
		}

		got, err := store.GetItem(ctx, tenantID, itemID)
		if err != nil {
			t.Fatalf("round %d: get item: %v", round, err)
		}
		// Two valid end states depending on commit order:
		//   - schedule committed first, immediate committed second → plan=C,
		//     pending cleared (immediate supersedes).
		//   - immediate committed first, schedule committed second → plan=C,
		//     pending=B (the schedule simply layered on after the swap).
		// Invalid state: plan=A (the swap got lost) — this is the regression
		// we're guarding against.
		if got.PlanID == planA.ID {
			t.Errorf("round %d: immediate swap was lost; plan_id reverted to A", round)
		}
		if got.PlanID != planC.ID {
			t.Errorf("round %d: final plan_id = %q, want %q", round, got.PlanID, planC.ID)
		}
	}
}

// seedActiveSubscription creates the FK chain (customer → plan → subscription)
// and returns an active subscription's ID ready for state-transition testing.
func seedActiveSubscription(t *testing.T, db *postgres.DB, tenantID, custExt, planCode, subCode string) string {
	t.Helper()
	ctx := context.Background()

	cust, err := customer.NewPostgresStore(db).Create(ctx, tenantID, domain.Customer{
		ExternalID: custExt, DisplayName: "Transition Tester",
	})
	if err != nil {
		t.Fatalf("create customer: %v", err)
	}

	plan, err := pricing.NewPostgresStore(db).CreatePlan(ctx, tenantID, domain.Plan{
		Code: planCode, Name: "Transition Plan", Currency: "USD",
		BillingInterval: domain.BillingMonthly, Status: domain.PlanActive,
		BaseAmountCents: 0,
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}

	now := time.Now().UTC()
	sub, err := subscription.NewPostgresStore(db).Create(ctx, tenantID, domain.Subscription{
		Code: subCode, DisplayName: "Transition Sub",
		CustomerID: cust.ID,
		Status:     domain.SubscriptionActive, BillingTime: domain.BillingTimeCalendar,
		StartedAt: &now,
		Items:     []domain.SubscriptionItem{{PlanID: plan.ID, Quantity: 1}},
	})
	if err != nil {
		t.Fatalf("create subscription: %v", err)
	}
	return sub.ID
}
