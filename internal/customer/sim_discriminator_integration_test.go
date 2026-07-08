package customer_test

import (
	"context"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/customer"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
	"github.com/sagarsuperuser/velox/internal/testutil"
)

// A clock-pinned customer carries a DURABLE is_simulated (generated from the
// immutable sim_clock_id, ADR-086 / migration 0143). The point of the durable
// stamp: it SURVIVES clock-delete detach (which nulls test_clock_id), so the
// operator dashboard's is_simulated=false analytics gate can never re-count the
// simulated row once its clock is gone. This is the vertical proof — stamp at
// create → true; detach → STILL true → excluded from the new_customers metric.
func TestCustomerSimDiscriminator_SurvivesDetach_AndGatesAnalytics(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx := postgres.WithLivemode(context.Background(), false)
	tenantID := testutil.CreateTestTenant(t, db, "Customer Sim Discriminator")

	execTx := func(sql string, args ...any) {
		t.Helper()
		tx, err := db.BeginTx(ctx, postgres.TxTenant, tenantID)
		if err != nil {
			t.Fatalf("begin tx: %v", err)
		}
		if _, err := tx.ExecContext(ctx, sql, args...); err != nil {
			t.Fatalf("exec %q: %v", sql, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit: %v", err)
		}
	}
	simulated := func(customerID string) bool {
		t.Helper()
		tx, err := db.BeginTx(ctx, postgres.TxTenant, tenantID)
		if err != nil {
			t.Fatalf("begin tx: %v", err)
		}
		defer postgres.Rollback(tx)
		var flag bool
		if err := tx.QueryRowContext(ctx, `SELECT is_simulated FROM customers WHERE id = $1`, customerID).Scan(&flag); err != nil {
			t.Fatalf("read is_simulated: %v", err)
		}
		return flag
	}

	// A live test clock (FK target for customers.test_clock_id).
	clockID := "vlx_tclk_simdisc0001"
	execTx(`INSERT INTO test_clocks (id, tenant_id, frozen_time) VALUES ($1, $2, now())`, clockID, tenantID)

	store := customer.NewPostgresStore(db)
	sim, err := store.Create(ctx, tenantID, domain.Customer{ExternalID: "cus_sim", DisplayName: "Sim", TestClockID: clockID})
	if err != nil {
		t.Fatalf("create clock-pinned customer: %v", err)
	}
	real, err := store.Create(ctx, tenantID, domain.Customer{ExternalID: "cus_real", DisplayName: "Real"})
	if err != nil {
		t.Fatalf("create wall-clock customer: %v", err)
	}

	// Stamp at create: the pinned customer is simulated, the wall-clock one is not.
	if !simulated(sim.ID) {
		t.Fatal("a clock-pinned customer must be is_simulated=true at creation")
	}
	if simulated(real.ID) {
		t.Fatal("a wall-clock customer must be is_simulated=false")
	}

	// Detach (what clock-delete does to customers.test_clock_id). The durable
	// sim_clock_id is untouched, so is_simulated must STILL be true — the whole
	// reason the discriminator isn't the mutable pin.
	execTx(`UPDATE customers SET test_clock_id = NULL WHERE id = $1`, sim.ID)
	if !simulated(sim.ID) {
		t.Fatal("is_simulated must SURVIVE detach — sim_clock_id is immutable, unlike the test_clock_id pin")
	}

	// The analytics new_customers gate excludes the simulated customer (even
	// though detached) and counts only the wall-clock one.
	tx, err := db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		t.Fatalf("begin count tx: %v", err)
	}
	defer postgres.Rollback(tx)
	var count int
	from, to := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM customers WHERE created_at >= $1 AND created_at < $2 AND is_simulated = false`,
		from, to).Scan(&count); err != nil {
		t.Fatalf("new_customers gate query: %v", err)
	}
	if count != 1 {
		t.Errorf("new_customers gate must count only the wall-clock customer (exclude the simulated one): got %d, want 1", count)
	}
}
