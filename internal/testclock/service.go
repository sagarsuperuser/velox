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
// persistence and (optionally) BillingRunner to kick off a billing catchup
// when the clock advances. When billing is nil, advance just updates
// frozen_time without emitting invoices — useful for narrow unit tests.
type Service struct {
	store   Store
	billing BillingRunner
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

type AdvanceInput struct {
	FrozenTime time.Time `json:"frozen_time"`
}

// Advance moves the clock forward to FrozenTime and synchronously catches
// up billing for every subscription attached to the clock. Catchup runs the
// billing engine in a loop because a large jump (e.g. 3 months forward on a
// monthly sub) closes multiple cycles — each engine sweep processes the
// cycles that are now due, advances next_billing_at, and the next sweep
// picks up the following cycle.
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

	if _, err := s.store.MarkAdvancing(ctx, tenantID, id, newTime); err != nil {
		return domain.TestClock{}, err
	}

	if s.billing != nil {
		if err := s.runCatchup(ctx, tenantID, id); err != nil {
			// Flip to internal_failure and surface the error. The clock's
			// frozen_time stays at the new value — partial catchup is still
			// applied, and the tenant can inspect invoices and delete the
			// clock to unstick themselves.
			if _, ferr := s.store.MarkFailed(ctx, tenantID, id); ferr != nil {
				slog.Error("test clock: failed to mark clock as failed after catchup error",
					"clock_id", id, "catchup_err", err, "mark_err", ferr)
			}
			return domain.TestClock{}, fmt.Errorf("billing catchup failed: %w", err)
		}
	}

	return s.store.CompleteAdvance(ctx, tenantID, id)
}

// runCatchup repeatedly runs the billing engine until no more subs on this
// clock come back as "due". Stops early on MaxAdvanceCatchupLoops to avoid
// an infinite loop if some bug kept producing "due" results.
func (s *Service) runCatchup(ctx context.Context, tenantID, clockID string) error {
	for range MaxAdvanceCatchupLoops {
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
