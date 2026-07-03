package subscription

import (
	"context"
	"testing"

	"github.com/sagarsuperuser/velox/internal/customer"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
	"github.com/sagarsuperuser/velox/internal/pricing"
	"github.com/sagarsuperuser/velox/internal/tenant"
	"github.com/sagarsuperuser/velox/internal/testutil"
)

// TestBillingTimezone_PersistsAndImmutable is the real-Postgres lock on the
// store half of the ADR-074 snapshot: CreateWithBill's INSERT must persist
// billing_timezone (the memStore unit test can't catch a dropped column —
// it echoes fields, exactly the Finding-1 activated_at trap), AND the
// snapshot must survive a later tenant-timezone change.
//
// Mutation-verify: remove billing_timezone from the CreateWithBill INSERT
// column list (postgres.go) — the persist assertion fails; the unit test
// stays green.
func TestBillingTimezone_PersistsAndImmutable(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx := postgres.WithLivemode(context.Background(), false)
	tenantID := testutil.CreateTestTenant(t, db, "TZ Snapshot")

	settingsStore := tenant.NewSettingsStore(db)
	if _, err := settingsStore.Upsert(ctx, domain.TenantSettings{
		TenantID: tenantID, DefaultCurrency: "USD", Timezone: "Asia/Kolkata",
		InvoicePrefix: "VLX", NetPaymentTerms: 30, TaxProvider: "manual", TaxOnFailure: "block",
	}); err != nil {
		t.Fatalf("set tenant TZ: %v", err)
	}

	cust, err := customer.NewPostgresStore(db).Create(ctx, tenantID, domain.Customer{
		ExternalID: "cus_tz", DisplayName: "TZ",
	})
	if err != nil {
		t.Fatalf("create customer: %v", err)
	}
	plan, err := pricing.NewPostgresStore(db).CreatePlan(ctx, tenantID, domain.Plan{
		Code: "tz-monthly", Name: "TZ Plan", Currency: "USD",
		BillingInterval: domain.BillingMonthly, BaseAmountCents: 2900,
		Status: domain.PlanActive, MeterIDs: []string{},
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}

	svc := NewService(NewPostgresStore(db), nil)
	svc.SetSettingsReader(settingsStore)

	sub, err := svc.Create(ctx, tenantID, CreateInput{
		Code: "tz-sub", DisplayName: "TZ Sub", CustomerID: cust.ID,
		Items:    []CreateItemInput{{PlanID: plan.ID}},
		StartNow: true,
	})
	if err != nil {
		t.Fatalf("create sub: %v", err)
	}
	if sub.BillingTimezone != "Asia/Kolkata" {
		t.Fatalf("returned BillingTimezone: got %q, want Asia/Kolkata (snapshot)", sub.BillingTimezone)
	}

	// Persisted (read via a fresh bypass tx, not the returned struct).
	btx, err := db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		t.Fatalf("bypass: %v", err)
	}
	defer postgres.Rollback(btx)
	var storedTZ string
	if err := btx.QueryRow(`SELECT billing_timezone FROM subscriptions WHERE id=$1`, sub.ID).Scan(&storedTZ); err != nil {
		t.Fatalf("read billing_timezone: %v", err)
	}
	if storedTZ != "Asia/Kolkata" {
		t.Fatalf("persisted billing_timezone: got %q, want Asia/Kolkata — the INSERT dropped the column", storedTZ)
	}

	// The operator now changes the TENANT timezone.
	if _, err := settingsStore.Upsert(ctx, domain.TenantSettings{
		TenantID: tenantID, DefaultCurrency: "USD", Timezone: "America/New_York",
		InvoicePrefix: "VLX", NetPaymentTerms: 30, TaxProvider: "manual", TaxOnFailure: "block",
	}); err != nil {
		t.Fatalf("change tenant TZ: %v", err)
	}

	// The running subscription keeps its snapshot — immutable.
	reread, err := NewPostgresStore(db).Get(ctx, tenantID, sub.ID)
	if err != nil {
		t.Fatalf("re-read sub: %v", err)
	}
	if reread.BillingTimezone != "Asia/Kolkata" {
		t.Fatalf("after tenant TZ change → America/New_York, sub billing_timezone: got %q, want Asia/Kolkata (must not re-anchor a running sub)", reread.BillingTimezone)
	}
}
