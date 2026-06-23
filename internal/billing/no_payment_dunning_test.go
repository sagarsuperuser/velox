package billing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// recordingDunningStarter records every StartDunning call so a test can
// assert which invoices got enrolled. When err is set it's returned for
// every call (simulating a dunning failure).
type recordingDunningStarter struct {
	started []string
	err     error
}

func (d *recordingDunningStarter) StartDunning(_ context.Context, _, invoiceID, _ string, _ time.Time) error {
	if d.err != nil {
		return d.err
	}
	d.started = append(d.started, invoiceID)
	return nil
}

func noPMEngine(t *testing.T, inv *mockInvoices) *Engine {
	t.Helper()
	return wireBaseTax(NewEngine(
		&mockSubs{cycleUpdated: make(map[string]bool)},
		&mockUsage{}, &mockPricing{}, inv, nil, &mockSettings{},
		&fakePaymentSetups{}, &recordingCharger{}, billingTestClock(),
	))
}

// TestEnrollStalledForDunning_EnrollsCardlessInvoice locks the no-card
// limbo fix: a finalized, auto_charge_pending invoice (no payment method)
// is enrolled into dunning by the sweep, so it reaches a terminal instead
// of being retried by RetryPendingCharges forever with nothing to charge.
func TestEnrollStalledForDunning_EnrollsCardlessInvoice(t *testing.T) {
	inv := &mockInvoices{invoices: []domain.Invoice{pendingInvoice()}}
	engine := noPMEngine(t, inv)
	starter := &recordingDunningStarter{}
	engine.SetDunningStarter(starter)

	swept, errsOut := engine.EnrollStalledForDunning(context.Background(), 10)
	if len(errsOut) != 0 {
		t.Fatalf("unexpected errors: %v", errsOut)
	}
	if swept != 1 {
		t.Fatalf("swept = %d, want 1", swept)
	}
	if len(starter.started) != 1 || starter.started[0] != "inv_1" {
		t.Fatalf("StartDunning calls = %v, want [inv_1]", starter.started)
	}
}

// TestEnrollStalledForDunning_NoStarterIsInert verifies the sweep is a
// no-op when no DunningStarter is wired (local dev / integration tests) —
// it must not panic or touch the invoice store.
func TestEnrollStalledForDunning_NoStarterIsInert(t *testing.T) {
	inv := &mockInvoices{invoices: []domain.Invoice{pendingInvoice()}}
	engine := noPMEngine(t, inv) // no SetDunningStarter

	swept, errsOut := engine.EnrollStalledForDunning(context.Background(), 10)
	if swept != 0 || len(errsOut) != 0 {
		t.Fatalf("expected inert no-op, got swept=%d errs=%v", swept, errsOut)
	}
}

// TestEnrollStalledForDunning_CollectsPerInvoiceErrors verifies one bad
// row doesn't abort the sweep: the error is collected and reported, and
// the sweep returns it rather than panicking.
func TestEnrollStalledForDunning_CollectsPerInvoiceErrors(t *testing.T) {
	inv := &mockInvoices{invoices: []domain.Invoice{pendingInvoice()}}
	engine := noPMEngine(t, inv)
	engine.SetDunningStarter(&recordingDunningStarter{err: errors.New("create run failed")})

	swept, errsOut := engine.EnrollStalledForDunning(context.Background(), 10)
	if swept != 0 {
		t.Fatalf("swept = %d, want 0 (the only candidate errored)", swept)
	}
	if len(errsOut) != 1 {
		t.Fatalf("errors = %v, want 1", errsOut)
	}
}
