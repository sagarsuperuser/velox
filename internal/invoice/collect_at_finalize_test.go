package invoice

import (
	"context"
	"errors"
	"testing"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/payment"
)

// fakeCharger records whether the auto-charge was attempted.
type fakeCharger struct {
	called bool
	err    error
}

func (f *fakeCharger) ChargeInvoice(_ context.Context, _ string, inv domain.Invoice, _, _ string) (domain.Invoice, error) {
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

func (f *fakeNoPMNotifier) NotifyNoPaymentMethod(_ context.Context, _ string, _ domain.Invoice) (domain.NotifyOutcome, error) {
	f.called = true
	return domain.NotifySent, nil
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
		svc:     NewService(store, nil, nil),
		charger: charger,
		paymentSetups: &fakePaymentSetups{setup: domain.CustomerPaymentSetup{
			SetupStatus: domain.PaymentSetupReady, StripeCustomerID: "cus_stripe_1", StripePaymentMethodID: "pm_1",
		}},
		noPMNotifier: notifier,
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

// A payment setup that reports "ready" but carries NO payment-method ID must
// take the not-ready arm (queue + notify), never the charge arm. The charge
// passes the PM ID verbatim and the charger hard-rejects an empty one — an
// error that lands in the decline arm, which sets no retry flag (dunning owns
// real declines), so charging here would dead-end the invoice with no retry
// path and no customer email. "Ready implies a PM ID" is an invariant of the
// current composite payment-setup reader, not of the interface: the previous
// reader (the dropped customer_payment_setups table) stored the two facts in
// independently-written columns, and this predicate is what keeps a future
// reader change from silently re-opening the dead-end.
func TestCollectAtFinalize_ReadyStatusWithoutPMID_QueuesInsteadOfCharging(t *testing.T) {
	t.Parallel()
	store := newMemStore()
	inv, err := store.Create(context.Background(), "t1", domain.Invoice{AmountDueCents: 5000, CustomerID: "cus_1"})
	if err != nil {
		t.Fatalf("seed invoice: %v", err)
	}
	charger := &fakeCharger{}
	notifier := &fakeNoPMNotifier{}
	h := &Handler{
		svc:     NewService(store, nil, nil),
		charger: charger,
		paymentSetups: &fakePaymentSetups{setup: domain.CustomerPaymentSetup{
			SetupStatus:      domain.PaymentSetupReady,
			StripeCustomerID: "cus_stripe_1",
			// StripePaymentMethodID deliberately empty.
		}},
		noPMNotifier: notifier,
	}

	h.collectAtFinalize(context.Background(), "t1", inv)

	if charger.called {
		t.Error("charger must NOT be called with an empty payment-method ID")
	}
	if !notifier.called {
		t.Error("expected the setup-link notifier — the customer has no chargeable card")
	}
	got, _ := store.Get(context.Background(), "t1", inv.ID)
	if !got.AutoChargePending {
		t.Error("expected auto_charge_pending=true so the sweep charges once a real PM exists")
	}
}

// readySetup is the canonical chargeable payment setup for decline-arm tests.
func readySetup() *fakePaymentSetups {
	return &fakePaymentSetups{setup: domain.CustomerPaymentSetup{
		SetupStatus: domain.PaymentSetupReady, StripeCustomerID: "cus_stripe_1", StripePaymentMethodID: "pm_1",
	}}
}

// declineArmFixture seeds one $50 invoice and a handler whose charger fails
// with chargeErr, runs collectAtFinalize, and reports whether the retry flag
// was set. The notifier must never fire on the PM-ready path.
func declineArmFixture(t *testing.T, chargeErr error) (flagSet bool) {
	t.Helper()
	store := newMemStore()
	inv, err := store.Create(context.Background(), "t1", domain.Invoice{AmountDueCents: 5000, CustomerID: "cus_1"})
	if err != nil {
		t.Fatalf("seed invoice: %v", err)
	}
	notifier := &fakeNoPMNotifier{}
	h := &Handler{
		svc:           NewService(store, nil, nil),
		charger:       &fakeCharger{err: chargeErr},
		paymentSetups: readySetup(),
		noPMNotifier:  notifier,
	}
	h.collectAtFinalize(context.Background(), "t1", inv)
	if notifier.called {
		t.Error("notifier must not fire on the PM-ready path (the customer has a card)")
	}
	got, _ := store.Get(context.Background(), "t1", inv.ID)
	return got.AutoChargePending
}

// TestCollectAtFinalize_DeclineOwnership pins WHO retries each charge-failure
// class after a manual finalize (ADR-087 follow-up). A definite decline is
// dunning's job — the charger starts a run inline — so the retry flag stays
// off (two owners minting distinct idempotency keys is a double-charge
// window). Every non-definite failure (breaker-open transient, ambiguous
// outcome, unclassified error) starts NO dunning, so the flag is the only
// retry path: pre-fix these dead-ended silently — no flag, no dunning, no
// email — and the invoice aged into overdue with nothing ever picking it up.
func TestCollectAtFinalize_DeclineOwnership(t *testing.T) {
	t.Parallel()

	t.Run("definite decline → no flag (dunning owns the retry)", func(t *testing.T) {
		t.Parallel()
		flagSet := declineArmFixture(t, &payment.PaymentError{Message: "card declined", DeclineCode: "card_declined"})
		if flagSet {
			t.Error("definite decline must NOT set auto_charge_pending — dunning is the single retry owner")
		}
	})

	t.Run("breaker-open transient → flag set (sweep re-drives next tick)", func(t *testing.T) {
		t.Parallel()
		flagSet := declineArmFixture(t, payment.ErrPaymentTransient)
		if !flagSet {
			t.Error("transient failure left no retry owner: flag must be set (invoice untouched, payment_status stays pending, sweep re-charges)")
		}
	})

	t.Run("ambiguous outcome → flag set (inert until the reconciler resolves)", func(t *testing.T) {
		t.Parallel()
		flagSet := declineArmFixture(t, &payment.PaymentError{Message: "stripe 5xx", Unknown: true})
		if !flagSet {
			t.Error("unknown outcome must set the flag: the sweep's payment_status='pending' predicate keeps it inert until the reconciler settles the true state")
		}
	})

	t.Run("unclassified error → flag set (conservative arm)", func(t *testing.T) {
		t.Parallel()
		flagSet := declineArmFixture(t, errors.New("wiring bug"))
		if !flagSet {
			t.Error("an unclassified charge error must queue for retry, never dead-end")
		}
	})
}

// ctxProbeCharger records the liveness and deadline of the ctx it is charged
// with — the real Stripe SDK aborts on a dead ctx, so these two facts are
// what decide whether a charge survives a client disconnect.
type ctxProbeCharger struct {
	called       bool
	ctxErrAtCall error
	hadDeadline  bool
}

func (c *ctxProbeCharger) ChargeInvoice(ctx context.Context, _ string, inv domain.Invoice, _, _ string) (domain.Invoice, error) {
	c.called = true
	c.ctxErrAtCall = ctx.Err()
	_, c.hadDeadline = ctx.Deadline()
	return inv, nil
}

// ctxProbeNotifier records ctx liveness at notify time (the setup-link email
// send would be aborted by a dead ctx in the real SMTP path).
type ctxProbeNotifier struct {
	called       bool
	ctxErrAtCall error
}

func (n *ctxProbeNotifier) NotifyNoPaymentMethod(ctx context.Context, _ string, _ domain.Invoice) (domain.NotifyOutcome, error) {
	n.called = true
	n.ctxErrAtCall = ctx.Err()
	return domain.NotifySent, nil
}

// TestCollectAtFinalize_SurvivesClientDisconnect pins the ctx-detach contract
// (ADR-087 follow-up): once the invoice is finalized, collection is not
// abortable by the operator's browser. Pre-fix the whole step ran on
// r.Context() — a client disconnect mid-charge cancelled the Stripe call at
// its most ambiguous moment AND killed the charger's 'unknown'
// outcome-persist, the retry-flag write, and the notifier in the same stroke:
// customer possibly charged, zero local record. The charge must also carry
// its own 30s deadline (engine parity) instead of riding the SDK default.
func TestCollectAtFinalize_SurvivesClientDisconnect(t *testing.T) {
	t.Parallel()

	t.Run("PM ready + cancelled request ctx → charge runs on a live, deadline-bounded ctx", func(t *testing.T) {
		t.Parallel()
		store := newMemStore()
		inv, err := store.Create(context.Background(), "t1", domain.Invoice{AmountDueCents: 5000, CustomerID: "cus_1"})
		if err != nil {
			t.Fatalf("seed invoice: %v", err)
		}
		charger := &ctxProbeCharger{}
		h := &Handler{
			svc:           NewService(store, nil, nil),
			charger:       charger,
			paymentSetups: readySetup(),
			noPMNotifier:  &fakeNoPMNotifier{},
		}

		reqCtx, cancel := context.WithCancel(context.Background())
		cancel() // the operator's tab is already gone

		h.collectAtFinalize(reqCtx, "t1", inv)

		if !charger.called {
			t.Fatal("charge must still be attempted after a client disconnect")
		}
		if charger.ctxErrAtCall != nil {
			t.Errorf("charge ctx must be detached from the request's cancellation, got err=%v", charger.ctxErrAtCall)
		}
		if !charger.hadDeadline {
			t.Error("charge ctx must carry its own deadline (engine-parity 30s), not ride the SDK default")
		}
	})

	t.Run("no PM + cancelled request ctx → flag and notify still delivered on a live ctx", func(t *testing.T) {
		t.Parallel()
		store := newMemStore()
		inv, err := store.Create(context.Background(), "t1", domain.Invoice{AmountDueCents: 5000, CustomerID: "cus_1"})
		if err != nil {
			t.Fatalf("seed invoice: %v", err)
		}
		notifier := &ctxProbeNotifier{}
		h := &Handler{
			svc:           NewService(store, nil, nil),
			charger:       &fakeCharger{},
			paymentSetups: &fakePaymentSetups{setup: domain.CustomerPaymentSetup{SetupStatus: domain.PaymentSetupMissing}},
			noPMNotifier:  notifier,
		}

		reqCtx, cancel := context.WithCancel(context.Background())
		cancel()

		h.collectAtFinalize(reqCtx, "t1", inv)

		if !notifier.called {
			t.Fatal("setup-link notify must still run after a client disconnect")
		}
		if notifier.ctxErrAtCall != nil {
			t.Errorf("notify ctx must be detached from the request's cancellation, got err=%v", notifier.ctxErrAtCall)
		}
		got, _ := store.Get(context.Background(), "t1", inv.ID)
		if !got.AutoChargePending {
			t.Error("retry flag must still land after a client disconnect")
		}
	})
}

// TestCollectAtFinalize_ZeroDue_AutoPaid is the manual writer's T12 twin
// (siblings: TestBillOnePeriod_ZeroTotal_AutoPaid, TestFireThreshold_ZeroTotal_
// AutoPaid). A finalized invoice with nothing left to pay has no payment to
// wait for — the terminal state is PAID (ADR-066; Stripe parity: zero-amount
// invoices auto-mark paid with no payment attempt). Pre-fix the collect
// step's amount_due<=0 early return stranded it finalized/payment_pending
// FOREVER: every charge path and the retry sweep gate on amount_due>0, and
// dunning never starts — a permanently-overdue attention item nothing could
// act on.
func TestCollectAtFinalize_ZeroDue_AutoPaid(t *testing.T) {
	t.Parallel()
	store := newMemStore()
	inv, err := store.Create(context.Background(), "t1", domain.Invoice{
		CustomerID: "cus_1", Status: domain.InvoiceFinalized,
		PaymentStatus: domain.PaymentPending, AmountDueCents: 0,
	})
	if err != nil {
		t.Fatalf("seed invoice: %v", err)
	}
	charger := &fakeCharger{}
	notifier := &fakeNoPMNotifier{}
	h := &Handler{
		svc:           NewService(store, nil, nil),
		charger:       charger,
		paymentSetups: readySetup(),
		noPMNotifier:  notifier,
	}

	out := h.collectAtFinalize(context.Background(), "t1", inv)

	if out.Status != domain.InvoicePaid || out.PaymentStatus != domain.PaymentSucceeded {
		t.Errorf("returned invoice = %s/%s, want paid/succeeded", out.Status, out.PaymentStatus)
	}
	got, _ := store.Get(context.Background(), "t1", inv.ID)
	if got.Status != domain.InvoicePaid || got.PaymentStatus != domain.PaymentSucceeded {
		t.Errorf("stored invoice = %s/%s, want paid/succeeded (zero-due must settle, never strand)", got.Status, got.PaymentStatus)
	}
	if charger.called {
		t.Error("nothing due → no charge attempt")
	}
	if notifier.called {
		t.Error("nothing due → no payment-method email")
	}
	if got.AutoChargePending {
		t.Error("nothing due → no retry flag")
	}
}

// A DRAFT zero-due invoice must never settle through SettleZeroDue — the
// DEMO-000906 class: a tax-pending draft jumping to paid blocks tax retry
// forever. Both the service re-read (status must be finalized) and the
// store's MarkPaid guard enforce it; this pins the service half.
func TestSettleZeroDue_DraftIsNotSettled(t *testing.T) {
	t.Parallel()
	store := newMemStore()
	inv, err := store.Create(context.Background(), "t1", domain.Invoice{
		CustomerID: "cus_1", Status: domain.InvoiceDraft,
		PaymentStatus: domain.PaymentPending, AmountDueCents: 0,
	})
	if err != nil {
		t.Fatalf("seed invoice: %v", err)
	}
	svc := NewService(store, nil, nil)

	out, err := svc.SettleZeroDue(context.Background(), "t1", inv.ID)
	if err != nil {
		t.Fatalf("SettleZeroDue on a draft must no-op, not error: %v", err)
	}
	if out.Status != domain.InvoiceDraft {
		t.Errorf("draft must stay draft, got %s", out.Status)
	}
	got, _ := store.Get(context.Background(), "t1", inv.ID)
	if got.Status != domain.InvoiceDraft || got.PaymentStatus != domain.PaymentPending {
		t.Errorf("stored draft mutated to %s/%s — must be untouched", got.Status, got.PaymentStatus)
	}
}
