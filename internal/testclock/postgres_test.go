package testclock_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
	"github.com/sagarsuperuser/velox/internal/testclock"
	"github.com/sagarsuperuser/velox/internal/testutil"
)

// TestPostgresStore_Delete_SoftDeletes_CascadesPinnedSubs covers
// the load-bearing ADR-016 behavior: deleting a clock soft-deletes
// the row (gone from List/Get) and cancels every pinned subscription
// whose status isn't already terminal.
func TestPostgresStore_Delete_SoftDeletes_CascadesPinnedSubs(t *testing.T) {
	db := testutil.SetupTestDB(t)
	store := testclock.NewPostgresStore(db)
	ctx := postgres.WithLivemode(context.Background(), false)
	tenantID := testutil.CreateTestTenant(t, db, "Tenant")

	clk, err := store.Create(ctx, tenantID, domain.TestClock{
		Name:       "trial",
		FrozenTime: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("create clock: %v", err)
	}

	// Insert two pinned subs directly. One active (should be
	// cascaded to canceled), one already-archived (should NOT be
	// touched — terminal-state preservation).
	customerID := insertCustomer(t, db, tenantID)
	insertSub(t, db, "sub_active", tenantID, customerID, clk.ID, "active")
	insertSub(t, db, "sub_archived", tenantID, customerID, clk.ID, "archived")

	if err := store.Delete(ctx, tenantID, clk.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// Get must now return ErrNotFound — soft-deleted hidden by
	// the live filter.
	if _, err := store.Get(ctx, tenantID, clk.ID); err != errs.ErrNotFound {
		t.Errorf("Get after delete: expected ErrNotFound, got %v", err)
	}

	clocks, err := store.List(ctx, tenantID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, c := range clocks {
		if c.ID == clk.ID {
			t.Errorf("List returned soft-deleted clock %s", c.ID)
		}
	}

	// Active sub canceled; archived sub untouched.
	if got := subStatus(t, db, "sub_active"); got != "canceled" {
		t.Errorf("sub_active status: got %q, want canceled", got)
	}
	if got := subStatus(t, db, "sub_archived"); got != "archived" {
		t.Errorf("sub_archived status (terminal preservation): got %q, want archived", got)
	}

	// Idempotent: re-deleting an already-deleted clock returns
	// ErrNotFound (the live filter hides it).
	if err := store.Delete(ctx, tenantID, clk.ID); err != errs.ErrNotFound {
		t.Errorf("repeat Delete: expected ErrNotFound, got %v", err)
	}
}

// TestPostgresStore_Delete_DetachesPinnedCustomers locks the fix for the
// customer-level stranding bug: when a clock is soft-deleted, customers
// pinned to it must be detached (test_clock_id → NULL), realizing the
// `ON DELETE SET NULL` the FK declares but soft-delete defeats. Without
// this, a customer keeps pointing at the dead clock and its NEXT
// subscription inherits it (ADR-027 customer-level pin) — landing
// stranded (excluded from both the wall-clock cron and the catchup path,
// so it never bills).
func TestPostgresStore_Delete_DetachesPinnedCustomers(t *testing.T) {
	db := testutil.SetupTestDB(t)
	store := testclock.NewPostgresStore(db)
	ctx := postgres.WithLivemode(context.Background(), false)
	tenantID := testutil.CreateTestTenant(t, db, "Tenant")

	clk, err := store.Create(ctx, tenantID, domain.TestClock{
		Name:       "trial",
		FrozenTime: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("create clock: %v", err)
	}

	// Customer pinned to the clock (the ADR-027 customer-level pin), plus
	// an active sub on it. A second, unpinned customer must be left alone.
	pinned := insertCustomer(t, db, tenantID)
	pinCustomerToClock(t, db, pinned, clk.ID)
	insertSub(t, db, "sub_active", tenantID, pinned, clk.ID, "active")
	other := insertCustomer(t, db, tenantID) // no pin

	if err := store.Delete(ctx, tenantID, clk.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// The pinned customer is detached — its next sub will be a clean
	// wall-clock sub, not a stranded dead-clock one.
	if got := customerClockID(t, db, pinned); got != "" {
		t.Errorf("pinned customer test_clock_id after delete: got %q, want empty (detached)", got)
	}
	// The sub is canceled (terminal) and KEEPS its pointer (denormalized
	// historical cache) — only customers detach.
	if got := subStatus(t, db, "sub_active"); got != "canceled" {
		t.Errorf("sub status: got %q, want canceled", got)
	}
	// The unrelated customer is untouched.
	if got := customerClockID(t, db, other); got != "" {
		t.Errorf("unrelated customer test_clock_id changed: got %q, want empty", got)
	}
}

// Helpers — minimal raw inserts so the test doesn't pull the
// subscription package's full Create surface (plans, items,
// currency) which the soft-delete behavior doesn't care about.
// Use TxBypass so the RLS predicate doesn't block fixture setup.

func pinCustomerToClock(t *testing.T, db *postgres.DB, customerID, clockID string) {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), postgres.TxBypass, "")
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer postgres.Rollback(tx)
	if _, err := tx.ExecContext(context.Background(),
		`SELECT set_config('app.livemode', 'off', true)`); err != nil {
		t.Fatalf("set livemode: %v", err)
	}
	if _, err := tx.ExecContext(context.Background(),
		`UPDATE customers SET test_clock_id = $2 WHERE id = $1`, customerID, clockID); err != nil {
		t.Fatalf("pin customer: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func customerClockID(t *testing.T, db *postgres.DB, customerID string) string {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), postgres.TxBypass, "")
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer postgres.Rollback(tx)
	var clockID sql.NullString
	if err := tx.QueryRowContext(context.Background(),
		`SELECT test_clock_id FROM customers WHERE id = $1`, customerID,
	).Scan(&clockID); err != nil {
		t.Fatalf("read customer test_clock_id: %v", err)
	}
	return clockID.String
}

func insertCustomer(t *testing.T, db *postgres.DB, tenantID string) string {
	t.Helper()
	id := "cus_test_" + randHex(8)
	tx, err := db.BeginTx(context.Background(), postgres.TxBypass, "")
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer postgres.Rollback(tx)
	if _, err := tx.ExecContext(context.Background(),
		`SELECT set_config('app.livemode', 'off', true)`); err != nil {
		t.Fatalf("set livemode: %v", err)
	}
	if _, err := tx.ExecContext(context.Background(), `
		INSERT INTO customers (id, tenant_id, external_id, display_name, livemode)
		VALUES ($1, $2, $1, 'test', false)
	`, id, tenantID); err != nil {
		t.Fatalf("insert customer: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return id
}

func insertSub(t *testing.T, db *postgres.DB, code, tenantID, customerID, clockID, status string) {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), postgres.TxBypass, "")
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer postgres.Rollback(tx)
	// Some BEFORE-INSERT triggers (api_keys, possibly subscriptions
	// via livemode propagation) read app.livemode and overwrite the
	// row's livemode column from it. TxBypass doesn't set this. Pin
	// it to test mode explicitly so the trigger doesn't reject the
	// row with the test_clock_requires_testmode CHECK.
	if _, err := tx.ExecContext(context.Background(),
		`SELECT set_config('app.livemode', 'off', true)`); err != nil {
		t.Fatalf("set livemode: %v", err)
	}
	if _, err := tx.ExecContext(context.Background(), `
		INSERT INTO subscriptions (id, tenant_id, code, display_name, customer_id, status, livemode, test_clock_id)
		VALUES ($1, $2, $3, $3, $4, $5, false, $6)
	`, "sub_test_"+code, tenantID, code, customerID, status, clockID); err != nil {
		t.Fatalf("insert sub: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func subStatus(t *testing.T, db *postgres.DB, code string) string {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), postgres.TxBypass, "")
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer postgres.Rollback(tx)
	var status string
	if err := tx.QueryRowContext(context.Background(),
		`SELECT status FROM subscriptions WHERE code = $1`, code,
	).Scan(&status); err != nil {
		t.Fatalf("read sub status: %v", err)
	}
	return status
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
