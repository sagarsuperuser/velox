package billing

import (
	"context"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/platform/clock"
)

// fakeEngineAudit records engine audit writes (the engine had no audit fake —
// the scheduled-cancel test asserts the webhook, not the row).
type fakeEngineAudit struct {
	rows []struct {
		action, resourceType, resourceID string
		meta                             map[string]any
		sim                              clock.Sim
		bound                            bool
	}
}

// Log captures the ctx CLOCK BINDING, not just the metadata: the sim axis is
// resolved by the audit writer from ctx (audit.simColumns) and by nothing else,
// so "did this row land on the sim axis?" is a question about the ctx the
// emission ran under. Asserting hand-stamped metadata keys instead would pin a
// mechanism that no longer exists.
func (f *fakeEngineAudit) Log(ctx context.Context, _, action, resourceType, resourceID, _ string, meta map[string]any) error {
	sim, bound := clock.SimOf(ctx)
	f.rows = append(f.rows, struct {
		action, resourceType, resourceID string
		meta                             map[string]any
		sim                              clock.Sim
		bound                            bool
	}{action, resourceType, resourceID, meta, sim, bound})
	return nil
}

// TestAuditInvoiceFinalized pins the engine-side finalize-audit contract: an
// engine-created FINALIZED invoice writes exactly one finalize row (the rows
// the TTFI metric reads); drafts (tax-pending / pause-collection) are skipped
// — they get their row from service.Finalize when the tax-retry chain
// finalizes them, and writing here too would double-count; clock-pinned subs
// carry sim context in metadata (ADR-030: created_at stays wall-clock).
func TestAuditInvoiceFinalized(t *testing.T) {
	now := time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC)
	inv := domain.Invoice{
		ID: "vlx_inv_1", InvoiceNumber: "VLX-000042", CustomerID: "cus_1",
		Status: domain.InvoiceFinalized, TotalAmountCents: 12345, Currency: "USD",
		BillingReason: domain.BillingReasonSubscriptionCycle,
	}

	t.Run("finalized invoice → one row with trigger", func(t *testing.T) {
		e, aud := &Engine{}, &fakeEngineAudit{}
		e.SetAuditLogger(aud)
		e.auditInvoiceFinalized(context.Background(), domain.Subscription{TenantID: "t1"}, inv, now)
		if len(aud.rows) != 1 {
			t.Fatalf("rows: got %d, want 1", len(aud.rows))
		}
		r := aud.rows[0]
		if r.action != string(domain.AuditActionFinalize) || r.resourceType != "invoice" || r.resourceID != "vlx_inv_1" {
			t.Errorf("row = %+v, want finalize/invoice/vlx_inv_1", r)
		}
		if r.meta["triggered_by"] != string(domain.BillingReasonSubscriptionCycle) {
			t.Errorf("triggered_by: got %v, want subscription_cycle", r.meta["triggered_by"])
		}
		if r.bound {
			t.Errorf("wall-clock sub emitted under a clock binding: %+v", r.sim)
		}
	})

	t.Run("draft invoice → no row (service.Finalize writes it later)", func(t *testing.T) {
		e, aud := &Engine{}, &fakeEngineAudit{}
		e.SetAuditLogger(aud)
		draft := inv
		draft.Status = domain.InvoiceDraft
		e.auditInvoiceFinalized(context.Background(), domain.Subscription{TenantID: "t1"}, draft, now)
		if len(aud.rows) != 0 {
			t.Fatalf("rows: got %d, want 0 for a draft", len(aud.rows))
		}
	})

	// Under a catchup the ctx arrives ALREADY bound (testclock.RunCatchup binds
	// it once for the advance; the engine never binds). This emission must run
	// under that binding and must not hand-stamp the axis into metadata — the
	// writer owns those keys, and an emitter that also wrote them would be
	// silently overwritten (that is how the trial-end instant used to get lost).
	t.Run("clock-pinned sub under a catchup ctx → the row inherits the binding", func(t *testing.T) {
		e, aud := &Engine{}, &fakeEngineAudit{}
		e.SetAuditLogger(aud)
		catchup := clock.WithSim(context.Background(), clock.Sim{At: now, TestClockID: "tclk_9"})

		e.auditInvoiceFinalized(catchup, domain.Subscription{TenantID: "t1", TestClockID: "tclk_9"}, inv, now)
		if len(aud.rows) != 1 {
			t.Fatalf("rows: got %d, want 1", len(aud.rows))
		}
		r := aud.rows[0]
		if !r.bound {
			t.Fatal("the emission ran on an UNBOUND ctx — the finalize row would land with NULL sim columns")
		}
		if r.sim.TestClockID != "tclk_9" || !r.sim.At.Equal(now) {
			t.Errorf("inherited sim = %+v, want {%s tclk_9}", r.sim, now)
		}
		for _, k := range []string{"sim_effective_at", "test_clock_id"} {
			if _, hand := r.meta[k]; hand {
				t.Errorf("emitter hand-stamped %q — the audit writer owns that key and overwrites it", k)
			}
		}
	})

	t.Run("nil logger → no panic", func(t *testing.T) {
		(&Engine{}).auditInvoiceFinalized(context.Background(), domain.Subscription{TenantID: "t1"}, inv, now)
	})
}
