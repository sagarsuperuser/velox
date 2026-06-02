package billing_test

import (
	"context"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/customer"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/invoice"
	"github.com/sagarsuperuser/velox/internal/platform/clock"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
	"github.com/sagarsuperuser/velox/internal/tenant"
	"github.com/sagarsuperuser/velox/internal/testutil"
)

// TestManualInvoice_IsSimulatedPersistedFromClockBinding is the wire-shape
// regression guard for the simulated-timestamp badge. It locks the fix for the
// bug where manual (operator-composed) invoices created on a test clock showed
// simulated dates but no badge — because the timeline re-derived simulation
// status from the (nonexistent) subscription instead of an authoritative flag.
//
// The contract: invoice.is_simulated is captured at write time from the
// creating context's clock binding (clock.IsSimulated(ctx)), persisted, and
// round-trips through the store. A clock-bound create stamps true; a
// wall-clock create stamps false. The timeline + header read this flag
// verbatim, so locking it here locks the badge.
//
// Binding ctx directly via clock.WithEffectiveNow simulates exactly what a
// clock-pinned customer entry point produces (bindForCreate resolves the
// customer pin to the same frozen instant); the service has no resolver wired
// here, so the pre-bound ctx is preserved unchanged.
func TestManualInvoice_IsSimulatedPersistedFromClockBinding(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test needs postgres")
	}
	db := testutil.SetupTestDB(t)
	ctx := postgres.WithLivemode(context.Background(), false)

	customerStore := customer.NewPostgresStore(db)
	invoiceStore := invoice.NewPostgresStore(db)
	settingsStore := tenant.NewSettingsStore(db)
	tenantID := testutil.CreateTestTenant(t, db, "Sim Flag Corp")

	cust, err := customerStore.Create(ctx, tenantID, domain.Customer{
		ExternalID: "cus_sim_flag", DisplayName: "Sim Flag Customer",
	})
	if err != nil {
		t.Fatalf("create customer: %v", err)
	}

	invoiceSvc := invoice.NewService(invoiceStore, clock.Real(), settingsStore)
	line := []invoice.AddLineItemInput{{Description: "Service", Quantity: 1, UnitAmountCents: 1000}}

	// (1) Wall-clock create → is_simulated must be false.
	wall, err := invoiceSvc.Create(ctx, tenantID, invoice.CreateInput{
		CustomerID: cust.ID, Currency: "USD", LineItems: line,
	})
	if err != nil {
		t.Fatalf("create wall-clock invoice: %v", err)
	}
	if wall.IsSimulated {
		t.Error("wall-clock manual invoice: is_simulated=true, want false")
	}

	// (2) Clock-bound create → is_simulated must be true and round-trip.
	frozen := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	simCtx := clock.WithEffectiveNow(ctx, frozen)
	sim, err := invoiceSvc.Create(simCtx, tenantID, invoice.CreateInput{
		CustomerID: cust.ID, Currency: "USD", LineItems: line,
	})
	if err != nil {
		t.Fatalf("create clock-bound invoice: %v", err)
	}
	if !sim.IsSimulated {
		t.Error("clock-bound manual invoice: is_simulated=false, want true")
	}
	// The domain timestamps must also land on simulated time (sanity: the same
	// binding that set the flag drives clock.Now).
	if sim.IssuedAt == nil || !sim.IssuedAt.Equal(frozen) {
		t.Errorf("issued_at: got %v, want frozen %v", sim.IssuedAt, frozen)
	}

	// (3) Round-trip: reload from the store (fresh read path) — the flag must
	// persist, not just live on the create-return value.
	reloaded, err := invoiceStore.Get(ctx, tenantID, sim.ID)
	if err != nil {
		t.Fatalf("reload invoice: %v", err)
	}
	if !reloaded.IsSimulated {
		t.Error("reloaded clock-bound invoice: is_simulated=false, want true (did not persist)")
	}
}
