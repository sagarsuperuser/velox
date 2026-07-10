package billing

import (
	"context"
	"errors"
	"testing"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// erroringPaymentSetups returns a resolve ERROR — the "can't determine PM
// state" case, distinct from a clean "no PM on file".
type erroringPaymentSetups struct{ err error }

func (f *erroringPaymentSetups) ResolveForCharge(_ context.Context, _, _ string) (string, string, error) {
	return "", "", f.err
}

// collectFixture builds an engine around one finalized $50 invoice and
// returns the pieces the pipeline touches. paymentSetups/charger default to
// wireBaseTax's no-PM/sentinel pair; tests override per arm.
func collectFixture() (*Engine, *mockInvoices, *fakeNoPMNotifier, domain.Subscription, domain.Invoice) {
	inv := domain.Invoice{
		ID: "inv_c1", TenantID: "t1", CustomerID: "cus_1",
		Status: domain.InvoiceFinalized, PaymentStatus: domain.PaymentPending,
		SubtotalCents: 5000, TotalAmountCents: 5000, AmountDueCents: 5000,
	}
	invoices := &mockInvoices{invoices: []domain.Invoice{inv}}
	e := wireBaseTax(NewEngine(&mockSubs{}, &mockUsage{}, &mockPricing{}, invoices, nil, &mockSettings{}, nil, nil, billingTestClock()))
	notifier := e.noPMNotifier.(*fakeNoPMNotifier)
	sub := domain.Subscription{ID: "sub_1", TenantID: "t1", CustomerID: "cus_1"}
	return e, invoices, notifier, sub, inv
}

func autoChargePending(t *testing.T, invoices *mockInvoices, id string) bool {
	t.Helper()
	for _, iv := range invoices.invoices {
		if iv.ID == id {
			return iv.AutoChargePending
		}
	}
	t.Fatalf("invoice %s not in mock", id)
	return false
}

// TestCollectAfterFinalize pins the shared post-finalize collection pipeline
// (2026-07-11 extraction of the four hand-copied site blocks) — in particular
// the three error-path behaviors that were silent per-site drift before:
// resolver-error ≠ no-PM (no false "payment method needed" email), reload
// failure queues instead of vanishing, and every downgrade lands on
// auto_charge_pending=true so the sweep re-drives it.
func TestCollectAfterFinalize(t *testing.T) {
	ctx := context.Background()

	t.Run("no PM on file → queued for sweep + setup-link email", func(t *testing.T) {
		e, invoices, notifier, sub, inv := collectFixture()

		e.collectAfterFinalize(ctx, sub, inv, "test")
		if !autoChargePending(t, invoices, inv.ID) {
			t.Error("no-PM arm must set auto_charge_pending=true")
		}
		if len(notifier.got) != 1 {
			t.Fatalf("notifier calls = %d, want 1", len(notifier.got))
		}
		if notifier.got[0].ID != inv.ID {
			t.Errorf("notified invoice = %s, want %s", notifier.got[0].ID, inv.ID)
		}
	})

	t.Run("resolver ERROR → queued, NO email (unknown ≠ missing)", func(t *testing.T) {
		// Pre-extraction all four sites conflated a transient resolve error
		// with "no payment method" and emailed a card-having customer a
		// setup link (design-census D10). The pipeline queues for the sweep
		// (which re-resolves each tick) and stays quiet.
		e, invoices, notifier, sub, inv := collectFixture()
		e.paymentSetups = &erroringPaymentSetups{err: errors.New("db blip")}

		e.collectAfterFinalize(ctx, sub, inv, "test")
		if !autoChargePending(t, invoices, inv.ID) {
			t.Error("resolver-error arm must set auto_charge_pending=true")
		}
		if len(notifier.got) != 0 {
			t.Errorf("resolver error must NOT email the customer, got %d notifies", len(notifier.got))
		}
	})

	t.Run("PM ready + reload fails → queued, no charge (was a silent vanish)", func(t *testing.T) {
		// Pre-extraction: no charge, no flag, no log — the invoice never
		// entered the retry path (design-census D7).
		e, invoices, notifier, sub, inv := collectFixture()
		e.paymentSetups = &fakePaymentSetups{ready: true, stripeCustomerID: "cus_stripe"}
		charger := &recordingCharger{}
		e.charger = charger
		invoices.getErr = errors.New("reload blip")

		e.collectAfterFinalize(ctx, sub, inv, "test")
		invoices.getErr = nil // let the flag assertion read the mock
		if len(charger.got) != 0 {
			t.Errorf("charge must not fire on a failed reload, got %d charges", len(charger.got))
		}
		if !autoChargePending(t, invoices, inv.ID) {
			t.Error("reload failure must queue for the sweep, not vanish")
		}
		if len(notifier.got) != 0 {
			t.Errorf("reload failure is not a no-PM state; got %d notifies", len(notifier.got))
		}
	})

	t.Run("PM ready → charges the RELOADED amount (post-credit truth)", func(t *testing.T) {
		// The caller's inv snapshot can carry a stale pre-credit amount_due;
		// the charge must see the store's current value.
		e, invoices, notifier, sub, inv := collectFixture()
		e.paymentSetups = &fakePaymentSetups{ready: true, stripeCustomerID: "cus_stripe"}
		charger := &recordingCharger{}
		e.charger = charger
		invoices.invoices[0].AmountDueCents = 1200 // credits shrank it after the snapshot

		e.collectAfterFinalize(ctx, sub, inv, "test") // inv still says 5000
		if len(charger.got) != 1 {
			t.Fatalf("charges = %d, want 1", len(charger.got))
		}
		if charger.got[0].AmountDueCents != 1200 {
			t.Errorf("charged amount_due = %d, want reloaded 1200 (never the stale snapshot 5000)", charger.got[0].AmountDueCents)
		}
		if autoChargePending(t, invoices, inv.ID) {
			t.Error("successful charge must not queue a retry")
		}
		if len(notifier.got) != 0 {
			t.Errorf("PM-ready path must not email, got %d", len(notifier.got))
		}
	})

	t.Run("PM ready + amount_due drained to 0 since snapshot → no charge, no flag", func(t *testing.T) {
		e, invoices, _, sub, inv := collectFixture()
		e.paymentSetups = &fakePaymentSetups{ready: true, stripeCustomerID: "cus_stripe"}
		charger := &recordingCharger{}
		e.charger = charger
		invoices.invoices[0].AmountDueCents = 0

		e.collectAfterFinalize(ctx, sub, inv, "test")
		if len(charger.got) != 0 {
			t.Errorf("nothing due → no charge, got %d", len(charger.got))
		}
		if autoChargePending(t, invoices, inv.ID) {
			t.Error("nothing due → no retry flag")
		}
	})

	t.Run("PM ready + decline → queued for sweep", func(t *testing.T) {
		e, invoices, _, sub, inv := collectFixture()
		e.paymentSetups = &fakePaymentSetups{ready: true, stripeCustomerID: "cus_stripe"}
		e.charger = &fakeChargerDecline{}

		e.collectAfterFinalize(ctx, sub, inv, "test")
		if !autoChargePending(t, invoices, inv.ID) {
			t.Error("decline must queue for the sweep (dunning starts inline in the charger)")
		}
	})

	t.Run("no PM + notify reload fails → still queued, notifier skipped, no panic", func(t *testing.T) {
		e, invoices, notifier, sub, inv := collectFixture()
		invoices.getErr = errors.New("reload blip")

		e.collectAfterFinalize(ctx, sub, inv, "test")
		invoices.getErr = nil
		if !autoChargePending(t, invoices, inv.ID) {
			t.Error("flag must be set before the notify reload")
		}
		if len(notifier.got) != 0 {
			t.Errorf("failed reload must skip the notifier, got %d", len(notifier.got))
		}
	})
}
