package testclock

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
)

// BillingRunner is the narrow hook the service uses to drive a billing
// catchup after a clock advance. In production the billing.Engine satisfies
// it (via RunCycle); tests can stub it with a spy that records calls. The
// contract: run one sweep of due subs and return how many invoices were
// produced plus any per-sub errors (non-fatal — failures on one sub must not
// stall the others).
type BillingRunner interface {
	RunCycle(ctx context.Context, batchSize int) (int, []error)
}

// MaxAdvanceCatchupLoops caps how many times we re-run the billing sweep
// after an advance. A monthly sub that jumps 5 years forward needs at least
// 60 passes to emit every invoice; cap is generous to allow long simulations
// while still terminating if a bug kept producing "due" subs indefinitely.
const MaxAdvanceCatchupLoops = 120

// Service provides the test-clock API surface. Depends on Store for
// persistence, optionally BillingRunner to drive the billing engine
// during catchup, and optionally CatchupQueue to dispatch catchup
// asynchronously after Advance. When the queue is wired, Advance
// returns as soon as the clock is marked advancing — a worker
// picks up the job and runs the catchup off the request path. When
// the queue is nil (narrow unit tests), Advance runs catchup
// inline so tests can assert end-state synchronously.
type Service struct {
	store   Store
	billing BillingRunner
	queue   CatchupQueue
}

func NewService(store Store) *Service {
	return &Service{store: store}
}

// SetBillingRunner wires the billing catchup hook. Kept as a setter rather
// than a constructor arg because the billing engine is built after the
// testclock service in router.go — the engine and service form a small
// dependency cycle (engine reads clocks, clock advance runs engine) that
// we break by deferred injection.
func (s *Service) SetBillingRunner(b BillingRunner) {
	s.billing = b
}

// SetCatchupQueue wires the async dispatch path. Production code
// always sets this; unit tests that want sync behaviour leave it
// nil so Advance runs catchup inline.
func (s *Service) SetCatchupQueue(q CatchupQueue) {
	s.queue = q
}

type CreateInput struct {
	Name       string    `json:"name"`
	FrozenTime time.Time `json:"frozen_time"`
}

func (s *Service) Create(ctx context.Context, tenantID string, input CreateInput) (domain.TestClock, error) {
	name := strings.TrimSpace(input.Name)
	if len(name) > 200 {
		return domain.TestClock{}, errs.Invalid("name", "must be at most 200 characters")
	}
	if input.FrozenTime.IsZero() {
		return domain.TestClock{}, errs.Required("frozen_time")
	}

	return s.store.Create(ctx, tenantID, domain.TestClock{
		Name:       name,
		FrozenTime: input.FrozenTime.UTC(),
	})
}

func (s *Service) Get(ctx context.Context, tenantID, id string) (domain.TestClock, error) {
	return s.store.Get(ctx, tenantID, id)
}

func (s *Service) List(ctx context.Context, tenantID string) ([]domain.TestClock, error) {
	return s.store.List(ctx, tenantID)
}

func (s *Service) Delete(ctx context.Context, tenantID, id string) error {
	return s.store.Delete(ctx, tenantID, id)
}

// SweepDueDeletes runs the TTL cleanup. Pass-through to the store
// so the scheduler tick (cmd/velox main wiring) doesn't need to
// reach across the store boundary directly.
func (s *Service) SweepDueDeletes(ctx context.Context, batch int) (int, error) {
	return s.store.SweepDueDeletes(ctx, batch)
}

// ListSubscriptions returns the subscriptions pinned to the given clock.
// Verifies the clock exists first so a missing-clock id surfaces as 404
// rather than an empty list (which would look like an empty clock).
func (s *Service) ListSubscriptions(ctx context.Context, tenantID, clockID string) ([]domain.Subscription, error) {
	if _, err := s.store.Get(ctx, tenantID, clockID); err != nil {
		return nil, err
	}
	return s.store.ListSubscriptionsOnClock(ctx, tenantID, clockID)
}

type AdvanceInput struct {
	FrozenTime time.Time `json:"frozen_time"`
}

// Advance moves the clock forward to FrozenTime and dispatches a
// billing catchup for every subscription attached to it. Catchup
// runs the billing engine in a loop because a large jump (e.g.
// 3 months forward on a monthly sub) closes multiple cycles —
// each engine sweep processes the cycles that are now due,
// advances next_billing_at, and the next sweep picks up the
// following cycle.
//
// Async dispatch: when SetCatchupQueue has been wired (the
// production path), Advance returns as soon as the clock is
// marked advancing and a CatchupJob has been enqueued. A worker
// picks up the job, runs the catchup, and flips the clock to
// ready / internal_failure when done. The dashboard polls
// /v1/test-clocks/{id} every 1.5s while status === 'advancing'
// to surface the transition. This matches Stripe's Test Clocks
// shape — the HTTP advance call returns in milliseconds, the
// catchup runs in the background.
//
// Sync fallback: when the queue is nil (narrow unit tests),
// Advance runs the catchup inline. RunCatchup contains the same
// logic the worker calls.
//
// State machine:
//
//	ready ──Advance── advancing ──catchup ok── ready
//	                       │
//	                       └──catchup errored── internal_failure
//
// While in advancing, other callers get 409 from the MarkAdvancing CAS; while
// in internal_failure, all further advances are blocked until the tenant
// inspects and deletes the clock.
func (s *Service) Advance(ctx context.Context, tenantID, id string, input AdvanceInput) (domain.TestClock, error) {
	if input.FrozenTime.IsZero() {
		return domain.TestClock{}, errs.Required("frozen_time")
	}
	newTime := input.FrozenTime.UTC()

	current, err := s.store.Get(ctx, tenantID, id)
	if err != nil {
		return domain.TestClock{}, err
	}
	if current.Status != domain.TestClockStatusReady {
		return domain.TestClock{}, errs.InvalidState(fmt.Sprintf("clock is %s, must be ready to advance", current.Status))
	}
	if !newTime.After(current.FrozenTime) {
		return domain.TestClock{}, errs.Invalid("frozen_time", "must be strictly after current frozen_time")
	}

	advancing, err := s.store.MarkAdvancing(ctx, tenantID, id, newTime)
	if err != nil {
		return domain.TestClock{}, err
	}

	if s.queue != nil {
		// Async path. The worker drains the queue and calls
		// RunCatchup. If enqueue fails (buffer full), revert to
		// internal_failure so the operator gets visible feedback
		// rather than a clock stuck in 'advancing' forever.
		if err := s.queue.Enqueue(CatchupJob{TenantID: tenantID, ClockID: id}); err != nil {
			if _, ferr := s.store.MarkFailed(ctx, tenantID, id, "catchup queue full: "+err.Error()); ferr != nil {
				slog.Error("test clock: failed to mark clock as failed after enqueue error",
					"clock_id", id, "enqueue_err", err, "mark_err", ferr)
			}
			return domain.TestClock{}, fmt.Errorf("dispatch catchup: %w", err)
		}
		return advancing, nil
	}

	// Sync fallback (tests / narrow setups without a queue).
	if err := s.RunCatchup(ctx, CatchupJob{TenantID: tenantID, ClockID: id}); err != nil {
		return domain.TestClock{}, err
	}
	return s.store.Get(ctx, tenantID, id)
}

// RetryAdvance resumes a clock parked in status='internal_failure'
// from a prior catchup error. Stripe-parity recovery — the catchup
// loop is idempotent (only processes subs with next_billing_at <=
// frozen_time), so resuming from where the previous attempt
// stopped is safe. Frozen_time stays at its current value; the
// operator's earlier Advance input is preserved by virtue of
// MarkAdvancing already having stamped frozen_time before the
// failure. ADR-018.
//
// Async dispatch: same as Advance — when SetCatchupQueue is
// wired, returns as soon as the clock is back in 'advancing' and
// a CatchupJob is enqueued. Worker drains; dashboard polls.
//
// Refuses to retry from any state other than internal_failure
// with a 409. A clock currently in 'advancing' has a worker
// already running on it; a clock in 'ready' has no failure to
// retry.
func (s *Service) RetryAdvance(ctx context.Context, tenantID, id string) (domain.TestClock, error) {
	current, err := s.store.Get(ctx, tenantID, id)
	if err != nil {
		return domain.TestClock{}, err
	}
	if current.Status != domain.TestClockStatusInternalFailed {
		return domain.TestClock{}, errs.InvalidState(fmt.Sprintf(
			"retry only valid on clocks in internal_failure (current: %s)", current.Status))
	}

	advancing, err := s.store.RetryFromFailed(ctx, tenantID, id)
	if err != nil {
		return domain.TestClock{}, err
	}

	if s.queue != nil {
		if err := s.queue.Enqueue(CatchupJob{TenantID: tenantID, ClockID: id}); err != nil {
			if _, ferr := s.store.MarkFailed(ctx, tenantID, id, "catchup queue full on retry: "+err.Error()); ferr != nil {
				slog.Error("test clock: failed to mark clock as failed after retry-enqueue error",
					"clock_id", id, "enqueue_err", err, "mark_err", ferr)
			}
			return domain.TestClock{}, fmt.Errorf("dispatch retry catchup: %w", err)
		}
		return advancing, nil
	}

	// Sync fallback (tests).
	if err := s.RunCatchup(ctx, CatchupJob{TenantID: tenantID, ClockID: id}); err != nil {
		return domain.TestClock{}, err
	}
	return s.store.Get(ctx, tenantID, id)
}

// RunCatchup is the worker's entry point. Repeatedly runs the
// billing engine until no more subs on this clock come back as
// "due". Stops early on MaxAdvanceCatchupLoops to avoid an
// infinite loop if some bug kept producing "due" results, and
// gets capped externally by the worker's wall-clock timeout
// (CatchupTimeout). On any error, flips the clock to
// internal_failure and returns the error so the worker can log it.
func (s *Service) RunCatchup(ctx context.Context, job CatchupJob) error {
	if s.billing == nil {
		// No billing wired — just complete the state transition.
		// Used by narrow unit tests that exercise the state machine
		// without standing up the full engine.
		_, err := s.store.CompleteAdvance(ctx, job.TenantID, job.ClockID)
		return err
	}

	if err := s.runCatchupLoop(ctx, job.TenantID, job.ClockID); err != nil {
		// Flip to internal_failure with the error captured so the
		// dashboard can show "Catchup failed: <reason>" without
		// forcing the operator to grep server logs. The clock's
		// frozen_time stays at the value it had — partial catchup
		// is still applied — and the operator can either Retry
		// advance (idempotent on subs) or delete the clock to
		// start fresh. ADR-018.
		if _, ferr := s.store.MarkFailed(ctx, job.TenantID, job.ClockID, err.Error()); ferr != nil {
			slog.Error("test clock: failed to mark clock as failed after catchup error",
				"clock_id", job.ClockID, "catchup_err", err, "mark_err", ferr)
		}
		return fmt.Errorf("billing catchup failed: %w", err)
	}

	if _, err := s.store.CompleteAdvance(ctx, job.TenantID, job.ClockID); err != nil {
		return fmt.Errorf("complete advance: %w", err)
	}
	return nil
}

// RecoverInFlight scans for clocks left in status='advancing' from
// a prior process — typically because the server restarted while a
// catchup was running — and re-enqueues them. Idempotent:
// runCatchupLoop only processes subs with next_billing_at <=
// frozen_time, so resuming partial work just continues from where
// it stopped.
//
// Called once on boot AFTER the worker is wired. If the queue is
// nil (test path), this is a no-op.
func (s *Service) RecoverInFlight(ctx context.Context) error {
	if s.queue == nil {
		return nil
	}
	clocks, err := s.store.ListAllAdvancing(ctx)
	if err != nil {
		return fmt.Errorf("list advancing clocks: %w", err)
	}
	for _, c := range clocks {
		if err := s.queue.Enqueue(CatchupJob{TenantID: c.TenantID, ClockID: c.ID}); err != nil {
			slog.Error("test clock: failed to re-enqueue in-flight catchup on boot",
				"clock_id", c.ID, "tenant_id", c.TenantID, "error", err)
			continue
		}
		slog.Info("test clock: re-enqueued in-flight catchup on boot",
			"clock_id", c.ID, "tenant_id", c.TenantID)
	}
	return nil
}

// runCatchupLoop is the inner loop. Extracted from the previous
// runCatchup so RunCatchup can wrap it with state-flip handling.
func (s *Service) runCatchupLoop(ctx context.Context, tenantID, clockID string) error {
	for range MaxAdvanceCatchupLoops {
		// Honour ctx deadline (the worker wraps with CatchupTimeout).
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("catchup ctx done: %w", err)
		}

		subs, err := s.store.ListSubscriptionsOnClock(ctx, tenantID, clockID)
		if err != nil {
			return fmt.Errorf("list subscriptions on clock: %w", err)
		}
		anyDue := false
		for _, sub := range subs {
			if sub.NextBillingAt == nil {
				continue
			}
			// We'd need the clock's frozen_time to decide; reload once per pass.
			clk, err := s.store.Get(ctx, tenantID, clockID)
			if err != nil {
				return err
			}
			if !sub.NextBillingAt.After(clk.FrozenTime) {
				anyDue = true
				break
			}
		}
		if !anyDue {
			return nil
		}

		n, runErrs := s.billing.RunCycle(ctx, 100)
		if len(runErrs) > 0 {
			return fmt.Errorf("billing run errors: %v", runErrs)
		}
		if n == 0 {
			// Billing didn't pick anything up despite the earlier check —
			// likely the subs moved out of active state. Stop to avoid a
			// busy loop.
			return nil
		}
	}
	return fmt.Errorf("billing catchup exceeded %d passes", MaxAdvanceCatchupLoops)
}
