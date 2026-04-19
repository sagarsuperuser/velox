package subscription_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/customer"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
	"github.com/sagarsuperuser/velox/internal/pricing"
	"github.com/sagarsuperuser/velox/internal/subscription"
	"github.com/sagarsuperuser/velox/internal/testutil"
)

// TestSubscriptions_OneLivePerCustomerPlan locks in HYG-2: a customer cannot
// hold two simultaneously-live subscriptions on the same plan. "Live" means
// status in {active, paused}. Terminal statuses (canceled, archived) must
// free the (customer, plan) pair so it can be re-subscribed.
func TestSubscriptions_OneLivePerCustomerPlan(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	tenantID := testutil.CreateTestTenant(t, db, "Sub Unique")

	cust, err := customer.NewPostgresStore(db).Create(ctx, tenantID, domain.Customer{
		ExternalID: "cus_unique_test", DisplayName: "Unique Co",
	})
	if err != nil {
		t.Fatalf("create customer: %v", err)
	}
	plan, err := pricing.NewPostgresStore(db).CreatePlan(ctx, tenantID, domain.Plan{
		Code: "plan-unique", Name: "Unique Plan", Currency: "USD",
		BillingInterval: domain.BillingMonthly, Status: domain.PlanActive,
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}

	store := subscription.NewPostgresStore(db)
	now := time.Now().UTC()

	// Seed an active subscription.
	_, err = store.Create(ctx, tenantID, domain.Subscription{
		Code: "sub-a", DisplayName: "A",
		CustomerID: cust.ID, PlanID: plan.ID,
		Status: domain.SubscriptionActive, BillingTime: domain.BillingTimeCalendar,
		StartedAt: &now,
	})
	if err != nil {
		t.Fatalf("seed active sub: %v", err)
	}

	// Second active sub on the same (customer, plan) must be rejected with a
	// friendly AlreadyExists error (not a bare PG constraint string).
	_, err = store.Create(ctx, tenantID, domain.Subscription{
		Code: "sub-b", DisplayName: "B",
		CustomerID: cust.ID, PlanID: plan.ID,
		Status: domain.SubscriptionActive, BillingTime: domain.BillingTimeCalendar,
		StartedAt: &now,
	})
	if err == nil {
		t.Fatal("expected error for duplicate active sub on same customer+plan")
	}
	if !errors.Is(err, errs.ErrAlreadyExists) {
		t.Errorf("expected AlreadyExists, got %T: %v", err, err)
	}

	// Paused state is also live — still blocks a parallel active row. Bypass the
	// store's transition helpers and flip the row directly so the test stays
	// focused on the DB constraint rather than on transition mechanics.
	tx, err := db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE subscriptions SET status = 'paused' WHERE tenant_id = $1 AND customer_id = $2`,
		tenantID, cust.ID,
	); err != nil {
		_ = tx.Rollback()
		t.Fatalf("pause sub: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit pause: %v", err)
	}

	_, err = store.Create(ctx, tenantID, domain.Subscription{
		Code: "sub-c", DisplayName: "C",
		CustomerID: cust.ID, PlanID: plan.ID,
		Status: domain.SubscriptionActive, BillingTime: domain.BillingTimeCalendar,
		StartedAt: &now,
	})
	if err == nil {
		t.Fatal("expected error for new active sub while original is paused")
	}
	if !errors.Is(err, errs.ErrAlreadyExists) {
		t.Errorf("expected AlreadyExists while paused, got %T: %v", err, err)
	}

	// Cancel the paused row — terminal status must release the (customer,plan)
	// pair so a fresh subscription can be created.
	tx2, err := db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		t.Fatalf("begin tx2: %v", err)
	}
	if _, err := tx2.ExecContext(ctx,
		`UPDATE subscriptions SET status = 'canceled' WHERE tenant_id = $1 AND customer_id = $2`,
		tenantID, cust.ID,
	); err != nil {
		_ = tx2.Rollback()
		t.Fatalf("cancel sub: %v", err)
	}
	if err := tx2.Commit(); err != nil {
		t.Fatalf("commit cancel: %v", err)
	}

	_, err = store.Create(ctx, tenantID, domain.Subscription{
		Code: "sub-d", DisplayName: "D",
		CustomerID: cust.ID, PlanID: plan.ID,
		Status: domain.SubscriptionActive, BillingTime: domain.BillingTimeCalendar,
		StartedAt: &now,
	})
	if err != nil {
		t.Fatalf("create after cancel should succeed: %v", err)
	}
}
