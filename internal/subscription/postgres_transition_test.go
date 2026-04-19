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
		CustomerID: cust.ID, PlanID: plan.ID,
		Status: domain.SubscriptionActive, BillingTime: domain.BillingTimeCalendar,
		StartedAt: &now,
	})
	if err != nil {
		t.Fatalf("create subscription: %v", err)
	}
	return sub.ID
}
