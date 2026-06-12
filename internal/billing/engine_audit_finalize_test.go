package billing

import (
	"context"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// fakeEngineAudit records engine audit writes (the engine had no audit fake —
// the scheduled-cancel test asserts the webhook, not the row).
type fakeEngineAudit struct {
	rows []struct {
		action, resourceType, resourceID string
		meta                             map[string]any
	}
}

func (f *fakeEngineAudit) Log(_ context.Context, _, action, resourceType, resourceID, _ string, meta map[string]any) error {
	f.rows = append(f.rows, struct {
		action, resourceType, resourceID string
		meta                             map[string]any
	}{action, resourceType, resourceID, meta})
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
		if _, simPresent := r.meta["sim_effective_at"]; simPresent {
			t.Error("wall-clock sub must not carry sim_effective_at")
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

	t.Run("clock-pinned sub → sim context in metadata", func(t *testing.T) {
		e, aud := &Engine{}, &fakeEngineAudit{}
		e.SetAuditLogger(aud)
		e.auditInvoiceFinalized(context.Background(), domain.Subscription{TenantID: "t1", TestClockID: "tclk_9"}, inv, now)
		if len(aud.rows) != 1 {
			t.Fatalf("rows: got %d, want 1", len(aud.rows))
		}
		m := aud.rows[0].meta
		if m["test_clock_id"] != "tclk_9" || m["sim_effective_at"] != now.Format(time.RFC3339) {
			t.Errorf("sim context: got %+v", m)
		}
	})

	t.Run("nil logger → no panic", func(t *testing.T) {
		(&Engine{}).auditInvoiceFinalized(context.Background(), domain.Subscription{TenantID: "t1"}, inv, now)
	})
}
