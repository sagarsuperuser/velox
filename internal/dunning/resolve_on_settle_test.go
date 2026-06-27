package dunning

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// recordingRetrier counts RetryPayment calls so a test can prove whether the
// paid-pre-check short-circuited (retrier NOT called) or fell through (called).
type recordingRetrier struct{ calls int }

func (r *recordingRetrier) RetryPayment(_ context.Context, _, _, _ string) error {
	r.calls++
	return nil
}

// stubInvGet serves a fixed invoice (or error) as the dunning InvoiceGetter.
type stubInvGet struct {
	inv domain.Invoice
	err error
}

func (s stubInvGet) Get(_ context.Context, _, _ string) (domain.Invoice, error) {
	return s.inv, s.err
}

type recordingSubCanceler struct{ calls int }

func (c *recordingSubCanceler) Cancel(_ context.Context, _, _ string) error { c.calls++; return nil }

type recordingPauser struct{ calls int }

func (p *recordingPauser) PauseCollection(_ context.Context, _, _ string) error {
	p.calls++
	return nil
}

// dueRunAt seeds an active run for inv_1 that is due now (backdated
// next_action_at) with the given attempt count.
func dueRunAt(t *testing.T, store *memStore, svc *Service, attempt int) domain.InvoiceDunningRun {
	t.Helper()
	run, err := svc.StartDunning(context.Background(), "t1", "inv_1", "cus_1", time.Now())
	if err != nil {
		t.Fatalf("start dunning: %v", err)
	}
	run.AttemptCount = attempt
	past := time.Now().UTC().Add(-time.Hour)
	run.NextActionAt = &past
	store.runs[run.ID] = run
	return run
}

func paidInv() domain.Invoice {
	return domain.Invoice{
		ID: "inv_1", TenantID: "t1", SubscriptionID: "sub_1",
		Status: domain.InvoicePaid, PaymentStatus: domain.PaymentSucceeded,
	}
}

// TestProcessRun_PaidInvoice_ResolvesWithoutRetrying: the paid-pre-check resolves
// a run whose invoice settled out-of-band as payment_recovered, WITHOUT calling
// the retrier or bumping the attempt. This is the backstop for the confirmed bug
// (a credit-cover sweep that MarkPaids without resolving).
func TestProcessRun_PaidInvoice_ResolvesWithoutRetrying(t *testing.T) {
	store := newMemStore()
	retrier := &recordingRetrier{}
	svc := NewService(store, retrier, nil)
	svc.SetSubscriptionPauser(&recordingPauser{}, stubInvGet{inv: paidInv()})

	run := dueRunAt(t, store, svc, 1)
	svc.ProcessDueRuns(context.Background(), "t1", 20)

	got := store.runs[run.ID]
	if got.State != domain.DunningResolved {
		t.Errorf("state: got %q, want resolved", got.State)
	}
	if got.Resolution != domain.ResolutionPaymentRecovered {
		t.Errorf("resolution: got %q, want payment_recovered", got.Resolution)
	}
	if got.AttemptCount != 1 {
		t.Errorf("attempt must not be bumped by the paid pre-check: got %d, want 1", got.AttemptCount)
	}
	if retrier.calls != 0 {
		t.Errorf("retrier must NOT run on a paid invoice: calls=%d", retrier.calls)
	}
}

// TestProcessRun_MaxRetriesPaid_DoesNotCancelSubscription is the load-bearing
// case: a run AT max retries whose invoice was settled out-of-band must NOT reach
// exhaustRun — which (final_action=cancel_subscription) would CANCEL a paying
// customer's subscription on a fully-paid invoice. The paid-pre-check must run
// BEFORE the max-retries→exhaustRun branch.
func TestProcessRun_MaxRetriesPaid_DoesNotCancelSubscription(t *testing.T) {
	store := newMemStore()
	p := store.policies[store.defaultID]
	p.FinalAction = domain.DunningActionCancelSubscription // would cancel if exhaustRun reached
	store.policies[store.defaultID] = p

	canceler := &recordingSubCanceler{}
	svc := NewService(store, &recordingRetrier{}, nil)
	svc.SetSubscriptionPauser(&recordingPauser{}, stubInvGet{inv: paidInv()})
	svc.SetSubscriptionCanceler(canceler)

	run := dueRunAt(t, store, svc, 3) // == default MaxRetryAttempts → would hit exhaustRun
	svc.ProcessDueRuns(context.Background(), "t1", 20)

	if canceler.calls != 0 {
		t.Fatalf("CANCELLED the subscription on a PAID invoice (Cancel calls=%d) — the paid-pre-check must precede the exhaustRun branch", canceler.calls)
	}
	got := store.runs[run.ID]
	if got.State != domain.DunningResolved || got.Resolution != domain.ResolutionPaymentRecovered {
		t.Errorf("max-retries paid run must resolve recovered: got state=%q resolution=%q", got.State, got.Resolution)
	}
}

// TestProcessRun_GatesOnStatusNotAmountDue: a finalized invoice that is NOT paid
// (PaymentPending) must NOT trip the pre-check even at $0 amount due (a
// mid-tax-retry draft can momentarily show $0) — it falls through to the normal
// retry path, proven by the retrier being called.
func TestProcessRun_GatesOnStatusNotAmountDue(t *testing.T) {
	store := newMemStore()
	retrier := &recordingRetrier{}
	svc := NewService(store, retrier, nil)
	inv := domain.Invoice{
		ID: "inv_1", TenantID: "t1",
		Status: domain.InvoiceFinalized, PaymentStatus: domain.PaymentPending, AmountDueCents: 0,
	}
	svc.SetSubscriptionPauser(&recordingPauser{}, stubInvGet{inv: inv})

	dueRunAt(t, store, svc, 1)
	svc.ProcessDueRuns(context.Background(), "t1", 20)

	if retrier.calls != 1 {
		t.Errorf("a finalized+pending invoice must fall through to retry (gate on STATUS, not amount_due): retrier calls=%d, want 1", retrier.calls)
	}
}

// TestProcessRun_VoidedInvoice_ResolvesManually: a voided invoice resolves the
// run as manually_resolved without retrying.
func TestProcessRun_VoidedInvoice_ResolvesManually(t *testing.T) {
	store := newMemStore()
	retrier := &recordingRetrier{}
	svc := NewService(store, retrier, nil)
	inv := domain.Invoice{ID: "inv_1", TenantID: "t1", Status: domain.InvoiceVoided}
	svc.SetSubscriptionPauser(&recordingPauser{}, stubInvGet{inv: inv})

	run := dueRunAt(t, store, svc, 1)
	svc.ProcessDueRuns(context.Background(), "t1", 20)

	got := store.runs[run.ID]
	if got.State != domain.DunningResolved || got.Resolution != domain.ResolutionManuallyResolved {
		t.Errorf("voided invoice: got state=%q resolution=%q, want resolved/manually_resolved", got.State, got.Resolution)
	}
	if retrier.calls != 0 {
		t.Errorf("retrier must not run on a voided invoice: calls=%d", retrier.calls)
	}
}

// TestProcessRun_InvoiceGetError_FallsThrough: a transient invoiceGet error must
// NOT resolve or skip — fall through to the normal retry path so a DB blip never
// burns or loses an attempt.
func TestProcessRun_InvoiceGetError_FallsThrough(t *testing.T) {
	store := newMemStore()
	retrier := &recordingRetrier{}
	svc := NewService(store, retrier, nil)
	svc.SetSubscriptionPauser(&recordingPauser{}, stubInvGet{err: errors.New("db blip")})

	dueRunAt(t, store, svc, 1)
	svc.ProcessDueRuns(context.Background(), "t1", 20)

	if retrier.calls != 1 {
		t.Errorf("invoiceGet error must fall through to retry: retrier calls=%d, want 1", retrier.calls)
	}
}
