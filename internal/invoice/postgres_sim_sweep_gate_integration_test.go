package invoice_test

import (
	"context"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/invoice"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
	"github.com/sagarsuperuser/velox/internal/testutil"
)

// The wall-clock auto-charge / dunning sweeps must exclude SIMULATED
// (test-clock) invoices by the invoice's own durable is_simulated flag — the
// ADR-029 invariant "the wall-clock scheduler never charges/dunns a simulated
// invoice". The prior subscriptions-join exclusion missed a customer-pinned
// one-off (NULL subscription_id → nothing to join through), which then leaked
// into these sweeps and got charged/enrolled against wall-clock time. These
// are the real-Postgres proofs that the durable-flag gate catches that case
// while leaving genuine wall-clock invoices eligible. Sim invoices are driven
// by the catchup counterparts (…ForClock) instead.

// seedSweepInvoice mutates a fresh draft invoice into the exact state a sweep
// scans for, in its own tenant (seedDraftInvoice pins a fixed customer
// external_id, so each row needs its own tenant; the sweeps scan cross-tenant
// under TxBypass, so one scan sees them all).
func seedSweepInvoice(t *testing.T, ctx context.Context, db *postgres.DB, name, sql string) string {
	t.Helper()
	tenantID := testutil.CreateTestTenant(t, db, name)
	id := seedDraftInvoice(t, db, tenantID)
	tx, err := db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		t.Fatalf("begin seed tx: %v", err)
	}
	if _, err := tx.ExecContext(ctx, sql, id); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit seed tx: %v", err)
	}
	return id
}

func TestListAutoChargePending_ExcludesSimulatedOneOff(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx := postgres.WithLivemode(context.Background(), false)
	store := invoice.NewPostgresStore(db)

	// A simulated customer-pinned ONE-OFF (NULL subscription_id) — the exact case
	// the subscriptions-join missed. Must be EXCLUDED from the auto-charge sweep.
	simOneOff := seedSweepInvoice(t, ctx, db, "AutoCharge Sim One-Off", `
		UPDATE invoices SET subscription_id = NULL, auto_charge_pending = TRUE,
		       payment_status = 'pending', status = 'finalized',
		       amount_due_cents = 1000, is_simulated = true, updated_at = now()
		 WHERE id = $1`)
	// A genuine wall-clock invoice — must be INCLUDED.
	realInv := seedSweepInvoice(t, ctx, db, "AutoCharge Real", `
		UPDATE invoices SET auto_charge_pending = TRUE, payment_status = 'pending',
		       status = 'finalized', amount_due_cents = 1000, is_simulated = false,
		       updated_at = now()
		 WHERE id = $1`)

	pending, err := store.ListAutoChargePending(ctx, 200)
	if err != nil {
		t.Fatalf("ListAutoChargePending: %v", err)
	}
	got := map[string]bool{}
	for _, inv := range pending {
		got[inv.ID] = true
	}
	if got[simOneOff] {
		t.Error("a simulated customer-pinned one-off must be excluded from the auto-charge sweep (ADR-029)")
	}
	if !got[realInv] {
		t.Error("a wall-clock invoice must remain eligible for the auto-charge sweep")
	}
}

func TestListFailedWithoutDunningRun_ExcludesSimulatedOneOff(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx := postgres.WithLivemode(context.Background(), false)
	store := invoice.NewPostgresStore(db)

	// updated_at is set two hours in the past so both rows satisfy the sweep's
	// `updated_at < olderThan` grace window (olderThan = now, below).
	simOneOff := seedSweepInvoice(t, ctx, db, "Dunning Enroll Sim One-Off", `
		UPDATE invoices SET subscription_id = NULL, payment_status = 'failed',
		       status = 'finalized', amount_due_cents = 1000, is_simulated = true,
		       updated_at = now() - interval '2 hours'
		 WHERE id = $1`)
	realInv := seedSweepInvoice(t, ctx, db, "Dunning Enroll Real", `
		UPDATE invoices SET payment_status = 'failed', status = 'finalized',
		       amount_due_cents = 1000, is_simulated = false,
		       updated_at = now() - interval '2 hours'
		 WHERE id = $1`)

	failed, err := store.ListFailedWithoutDunningRun(ctx, time.Now(), 200)
	if err != nil {
		t.Fatalf("ListFailedWithoutDunningRun: %v", err)
	}
	got := map[string]bool{}
	for _, inv := range failed {
		got[inv.ID] = true
	}
	if got[simOneOff] {
		t.Error("a simulated customer-pinned one-off must be excluded from dunning enrollment (ADR-029)")
	}
	if !got[realInv] {
		t.Error("a wall-clock failed invoice must remain eligible for dunning enrollment")
	}
}
