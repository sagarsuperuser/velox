package invoice

import (
	"context"
	"testing"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// fakeCharger records whether the auto-charge was attempted.
type fakeCharger struct {
	called bool
	err    error
}

func (f *fakeCharger) ChargeInvoice(_ context.Context, _ string, inv domain.Invoice, _ string) (domain.Invoice, error) {
	f.called = true
	if f.err != nil {
		return domain.Invoice{}, f.err
	}
	return inv, nil
}

// fakePaymentSetups returns a canned payment-setup snapshot.
type fakePaymentSetups struct {
	setup domain.CustomerPaymentSetup
	err   error
}

func (f *fakePaymentSetups) GetPaymentSetup(_ context.Context, _, _ string) (domain.CustomerPaymentSetup, error) {
	return f.setup, f.err
}

// fakeNoPMNotifier records whether the customer was notified.
type fakeNoPMNotifier struct {
	called bool
}

func (f *fakeNoPMNotifier) NotifyNoPaymentMethod(_ context.Context, _ string, _ domain.Invoice) error {
	f.called = true
	return nil
}

// When a manual invoice is finalized for a customer WITHOUT a saved card,
// collectAtFinalize must queue it for the scheduler's auto-charge retry AND
// notify the customer — matching the billing engine's cycle-invoice path.
// Pre-fix this case did nothing (silent overdue). This is the asymmetry the
// manual-vs-cycle audit flagged as the core "notifications look different".
func TestCollectAtFinalize_NoPaymentMethod_QueuesRetryAndNotifies(t *testing.T) {
	t.Parallel()
	store := newMemStore()
	inv, err := store.Create(context.Background(), "t1", domain.Invoice{AmountDueCents: 5000, CustomerID: "cus_1"})
	if err != nil {
		t.Fatalf("seed invoice: %v", err)
	}
	charger := &fakeCharger{}
	notifier := &fakeNoPMNotifier{}
	h := &Handler{
		svc:           NewService(store, nil, nil),
		charger:       charger,
		paymentSetups: &fakePaymentSetups{setup: domain.CustomerPaymentSetup{SetupStatus: domain.PaymentSetupMissing}},
		noPMNotifier:  notifier,
	}

	h.collectAtFinalize(context.Background(), "t1", inv)

	if charger.called {
		t.Error("charger must NOT be called when no payment method is ready")
	}
	if !notifier.called {
		t.Error("expected the no-payment-method notifier to be called")
	}
	got, _ := store.Get(context.Background(), "t1", inv.ID)
	if !got.AutoChargePending {
		t.Error("expected auto_charge_pending=true so the scheduler self-heals when a card is attached")
	}
}

// When a manual invoice is finalized for a customer WITH a saved card,
// collectAtFinalize auto-charges and does NOT notify or set the retry flag —
// the receipt fires from the Stripe webhook on success, dunning on decline.
func TestCollectAtFinalize_PaymentMethodReady_ChargesWithoutNotifying(t *testing.T) {
	t.Parallel()
	store := newMemStore()
	inv, err := store.Create(context.Background(), "t1", domain.Invoice{AmountDueCents: 5000, CustomerID: "cus_1"})
	if err != nil {
		t.Fatalf("seed invoice: %v", err)
	}
	charger := &fakeCharger{}
	notifier := &fakeNoPMNotifier{}
	h := &Handler{
		svc:           NewService(store, nil, nil),
		charger:       charger,
		paymentSetups: &fakePaymentSetups{setup: domain.CustomerPaymentSetup{SetupStatus: domain.PaymentSetupReady, StripeCustomerID: "cus_stripe_1"}},
		noPMNotifier:  notifier,
	}

	h.collectAtFinalize(context.Background(), "t1", inv)

	if !charger.called {
		t.Error("expected the saved card to be auto-charged when a payment method is ready")
	}
	if notifier.called {
		t.Error("no-payment-method notifier must NOT be called when a card is on file")
	}
	got, _ := store.Get(context.Background(), "t1", inv.ID)
	if got.AutoChargePending {
		t.Error("auto_charge_pending must stay false on the happy auto-charge path")
	}
}
