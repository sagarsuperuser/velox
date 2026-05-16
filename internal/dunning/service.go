package dunning

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/clock"
)

// ErrTransientSkip signals that the PaymentRetrier could not attempt a
// charge this tick — the Stripe call never happened, so dunning must NOT
// tick attempt_count or emit a failure event. Typical causes: per-tenant
// circuit breaker open, context timeout fired before the Stripe API call.
// The adapter that bridges payment → dunning maps payment's internal
// sentinel to this dunning-visible one so peer domains don't import each
// other.
var ErrTransientSkip = errors.New("dunning retry skipped: upstream transient")

// PaymentRetrier retries a payment for an invoice.
type PaymentRetrier interface {
	RetryPayment(ctx context.Context, tenantID, invoiceID, stripeCustomerID string) error
}

// SubscriptionPauser pauses a subscription when dunning exhausts retries.
type SubscriptionPauser interface {
	Pause(ctx context.Context, tenantID, id string) error
}

// InvoiceGetter gets invoice details for finding the subscription.
type InvoiceGetter interface {
	Get(ctx context.Context, tenantID, id string) (domain.Invoice, error)
}

// CustomerEmailFetcher resolves customer contact info for email notifications.
type CustomerEmailFetcher interface {
	GetCustomerEmail(ctx context.Context, tenantID, customerID string) (email, displayName string, err error)
}

// EmailNotifier sends dunning-related emails. publicToken is the
// hosted-invoice URL credential (T0-17) — pass empty when unavailable
// (drafts, pre-addendum invoices); the sender gracefully omits the CTA.
// ctx carries livemode for the underlying enqueue / brand lookup.
type EmailNotifier interface {
	SendPaymentFailed(ctx context.Context, tenantID, to, customerName, invoiceNumber, reason, publicToken string) error
	// SendDunningWarning emails the customer about a failed retry.
	// failureReason is the latest decline reason (from the retrier's
	// error message) — surfaced inline so customers can act
	// (insufficient_funds → top up; lost_card → swap card). Empty
	// reason renders without the diagnostic block. The template
	// branches on attemptNumber == maxAttempts for "Last attempt"
	// urgency tone.
	SendDunningWarning(ctx context.Context, tenantID, to, customerName, invoiceNumber string, attemptNumber, maxAttempts int, nextRetryDate, failureReason, publicToken string) error
	SendDunningEscalation(ctx context.Context, tenantID, to, customerName, invoiceNumber, action, publicToken string) error
}

type Service struct {
	store         Store
	retrier       PaymentRetrier
	subPauser     SubscriptionPauser
	invoiceGet    InvoiceGetter
	events        domain.EventDispatcher
	emailNotifier EmailNotifier
	customerEmail CustomerEmailFetcher
	resolver      clock.Resolver
	clock         clock.Clock
}

// SetResolver wires the unified clock.Resolver. Once bound on ctx via
// clock.BindEffectiveNow at the entry point of any per-invoice
// dunning operation, every downstream s.clock.Now(ctx) reads
// frozen_time on clock-pinned invoices. Optional: nil leaves binding
// off and every callsite reads wall-clock.
func (s *Service) SetResolver(r clock.Resolver) {
	s.resolver = r
}

// bindForInvoice binds effective-now from an invoice id at every
// dunning state-machine entry point (StartDunning, processRun,
// exhaustRun, ResolveRun, ResolveByInvoice). Returns ctx unchanged on
// resolver error or no resolver — wall-clock fallback.
func (s *Service) bindForInvoice(ctx context.Context, tenantID, invoiceID string) context.Context {
	bound, _ := clock.BindEffectiveNow(ctx, s.resolver, clock.Pin{TenantID: tenantID, InvoiceID: invoiceID})
	return bound
}

func NewService(store Store, retrier PaymentRetrier, clk clock.Clock) *Service {
	if clk == nil {
		clk = clock.Real()
	}
	return &Service{store: store, retrier: retrier, clock: clk}
}

func (s *Service) SetRetrier(retrier PaymentRetrier) {
	s.retrier = retrier
}

// SetSubscriptionPauser configures subscription pausing for dunning final actions.
func (s *Service) SetSubscriptionPauser(pauser SubscriptionPauser, invoices InvoiceGetter) {
	s.subPauser = pauser
	s.invoiceGet = invoices
}

// SetEmailNotifier configures email notifications for dunning events.
func (s *Service) SetEmailNotifier(notifier EmailNotifier) {
	s.emailNotifier = notifier
}

// SetCustomerEmailFetcher configures customer email resolution for dunning notifications.
func (s *Service) SetCustomerEmailFetcher(fetcher CustomerEmailFetcher) {
	s.customerEmail = fetcher
}

// SetEventDispatcher configures outbound webhook event firing.
func (s *Service) SetEventDispatcher(events domain.EventDispatcher) {
	s.events = events
}

// fireEvent dispatches a webhook event. Synchronous: with the outbox (RES-1)
// Dispatch is a short DB insert that must persist-before-return so a
// process crash can't silently drop the event.
func (s *Service) fireEvent(ctx context.Context, tenantID, eventType string, payload map[string]any) {
	if s.events == nil {
		return
	}
	if err := s.events.Dispatch(ctx, tenantID, eventType, payload); err != nil {
		slog.Error("dispatch dunning event",
			"event_type", eventType,
			"tenant_id", tenantID,
			"error", err,
		)
	}
}

// StartDunning initiates a dunning run for a failed invoice payment.
//
// One run per invoice, lifetime — Stripe-parity. The previous behaviour
// (idempotent only on ACTIVE runs, allowing a new run after an escalated
// or resolved one) produced duplicates on /dunning?tab=runs whenever
// a re-triggered payment failure landed on an already-terminal invoice.
// Escalated runs are terminal in our state machine; subsequent payment
// failures on the same invoice should NOT start a fresh campaign —
// the operator interprets the existing escalated run as the canonical
// record and resolves it manually if the customer pays.
//
// Returns the existing run regardless of state. The DB UNIQUE index
// (migration 0085) is the belt to this code's suspenders: even if a
// race somehow gets past this check, the INSERT in CreateRun fails
// with a constraint violation.
//
// failureAt is the simulated moment the charge failed — typically the
// invoice's cycle-close instant, NOT wall-clock-or-frozen "now." For
// clock-pinned invoices under catchup, frozen_time is advance-end
// (e.g. May 20) while the charge "actually" failed at the May 1 cycle
// close; anchoring next_action_at on frozen_time would push the first
// retry past advance-end and leave it stranded. The caller is
// responsible for resolving failureAt from invoice period boundaries.
// Pass time.Now() (or s.clock.Now(ctx)) when no period anchor is
// available — that's the wall-clock / manual-invoice case.
func (s *Service) StartDunning(ctx context.Context, tenantID string, invoiceID, customerID string, failureAt time.Time) (domain.InvoiceDunningRun, error) {
	existing, err := s.store.GetRunByInvoice(ctx, tenantID, invoiceID)
	if err == nil && existing.ID != "" {
		return existing, nil // Idempotent — return existing run regardless of state.
	}

	policy, err := s.store.GetPolicy(ctx, tenantID)
	if err != nil {
		return domain.InvoiceDunningRun{}, fmt.Errorf("get dunning policy: %w", err)
	}
	if !policy.Enabled {
		return domain.InvoiceDunningRun{}, errs.InvalidState("dunning is disabled for this tenant")
	}

	// Check for customer-specific dunning override
	if override, err := s.store.GetCustomerOverride(ctx, tenantID, customerID); err == nil {
		if override.MaxRetryAttempts != nil {
			policy.MaxRetryAttempts = *override.MaxRetryAttempts
		}
		if override.GracePeriodDays != nil {
			policy.GracePeriodDays = *override.GracePeriodDays
		}
		if override.FinalAction != "" {
			policy.FinalAction = domain.DunningFinalAction(override.FinalAction)
		}
	}

	// Grace period determines when the first retry happens.
	// retry_schedule determines the intervals between subsequent retries.
	firstRetryDelay := time.Duration(policy.GracePeriodDays) * 24 * time.Hour
	if firstRetryDelay <= 0 {
		firstRetryDelay = 24 * time.Hour // minimum 1 day before first retry
	}

	ctx = s.bindForInvoice(ctx, tenantID, invoiceID)
	if failureAt.IsZero() {
		// Defensive fallback — callers should always supply, but a missing
		// timestamp should not blow up dunning. Use the clock's "now",
		// which under catchup is advance-end frozen_time (the same
		// degenerate case we're trying to avoid, but at least the run
		// gets created so the operator can see it).
		failureAt = s.clock.Now(ctx)
	}
	t := failureAt.Add(firstRetryDelay)
	nextActionAt := &t

	run, err := s.store.CreateRun(ctx, tenantID, domain.InvoiceDunningRun{
		InvoiceID:    invoiceID,
		CustomerID:   customerID,
		PolicyID:     policy.ID,
		State:        domain.DunningActive,
		Reason:       "payment_failed",
		AttemptCount: 0,
		NextActionAt: nextActionAt,
		// CreatedAt = failureAt so the dunning run lives on simulated
		// cycle-close time, not orchestrator frozen_time. Aligns the
		// 'Automatic retry scheduled' row in the invoice timeline with
		// the cycle's period_end.
		CreatedAt: failureAt,
	})
	if err != nil {
		return domain.InvoiceDunningRun{}, fmt.Errorf("create dunning run: %w", err)
	}

	// Record start event at the simulated cycle-close instant so
	// the invoice timeline's 'Automatic retry scheduled' row aligns
	// with the cycle's period_end, not the orchestrator's frozen_time.
	_, _ = s.store.CreateEvent(ctx, tenantID, domain.InvoiceDunningEvent{
		RunID:     run.ID,
		InvoiceID: invoiceID,
		EventType: domain.DunningEventStarted,
		State:     domain.DunningActive,
		Reason:    "payment_failed",
		CreatedAt: failureAt,
	})

	slog.Info("dunning started", "run_id", run.ID, "invoice_id", invoiceID)

	s.fireEvent(ctx, tenantID, domain.EventDunningStarted, map[string]any{
		"run_id":      run.ID,
		"invoice_id":  invoiceID,
		"customer_id": customerID,
	})

	return run, nil
}

// ProcessDueRuns finds runs due for action and executes retries —
// CRON path. ADR-029 Phase 5: the cron's ListDueRuns excludes
// dunning runs whose owning invoice's sub is clock-pinned; those go
// through ProcessDueRunsForClock during catchup.
func (s *Service) ProcessDueRuns(ctx context.Context, tenantID string, limit int) (int, []error) {
	if limit <= 0 {
		limit = 20
	}
	dueRuns, err := s.store.ListDueRuns(ctx, tenantID, s.clock.Now(ctx), limit)
	if err != nil {
		return 0, []error{fmt.Errorf("list due runs: %w", err)}
	}
	return s.processRunsBatch(ctx, tenantID, dueRuns)
}

// ProcessDueRunsForClock is the catchup-path counterpart. ADR-029
// Phase 5: clock-pinned dunning runs advance only on operator Advance,
// against the clock's frozen_time. The dunning state machine itself
// (processRun) is identical between paths — only the candidate-fetch
// scope differs.
//
// Loops until ListDueRunsForClock returns zero rows or the safety cap
// hits — required because one run can advance through multiple retries
// in a single Advance click. Pre-fix, a single batch only fired the
// retry that was due at query time; the new next_action_at written
// by processRun was never re-queried, so the operator saw at most
// one retry per click even when several were due in the simulated
// window. Stripe Test Clocks parity: one Advance walks every
// time-driven action to completion.
//
// The re-query is bounded by:
//   - maxDunningCatchupIters: prevents pathological infinite loops if
//     processRun leaves a run in a non-progressing state (e.g.
//     persistent transient skip that rewinds attempt_count without
//     advancing next_action_at — would otherwise yield the same row
//     every iteration).
//   - Per-iteration progress check: if a run reappears with the same
//     attempt_count it had on the previous iteration, it didn't
//     advance — bail to avoid spinning.
const maxDunningCatchupIters = 50

func (s *Service) ProcessDueRunsForClock(ctx context.Context, tenantID, clockID string, frozenTime time.Time, limit int) (int, []error) {
	if limit <= 0 {
		limit = 20
	}
	total := 0
	var allErrs []error
	seen := make(map[string]int) // run_id → last-iteration attempt_count
	for iter := range maxDunningCatchupIters {
		if err := ctx.Err(); err != nil {
			return total, append(allErrs, fmt.Errorf("dunning catchup ctx done: %w", err))
		}
		dueRuns, err := s.store.ListDueRunsForClock(ctx, tenantID, clockID, frozenTime, limit)
		if err != nil {
			return total, append(allErrs, fmt.Errorf("list due runs for clock %s: %w", clockID, err))
		}
		if len(dueRuns) == 0 {
			return total, allErrs
		}
		if iter > 0 {
			anyProgress := false
			for _, r := range dueRuns {
				if r.AttemptCount > seen[r.ID] {
					anyProgress = true
					break
				}
			}
			if !anyProgress {
				slog.Warn("dunning catchup loop made no progress — exiting",
					"clock_id", clockID, "iter", iter, "remaining_due", len(dueRuns))
				return total, allErrs
			}
		}
		for _, r := range dueRuns {
			seen[r.ID] = r.AttemptCount
		}
		n, errs := s.processRunsBatch(ctx, tenantID, dueRuns)
		total += n
		allErrs = append(allErrs, errs...)
	}
	slog.Warn("dunning catchup loop hit safety cap — remaining runs deferred to next Advance",
		"clock_id", clockID, "cap", maxDunningCatchupIters, "processed", total)
	return total, allErrs
}

// processRunsBatch is the shared per-run body of ProcessDueRuns and
// ProcessDueRunsForClock. The candidate list shape differs by trigger;
// the per-run state-machine step is identical.
func (s *Service) processRunsBatch(ctx context.Context, tenantID string, dueRuns []domain.InvoiceDunningRun) (int, []error) {
	processed := 0
	var runErrs []error
	for _, run := range dueRuns {
		if err := s.processRun(ctx, tenantID, run); err != nil {
			runErrs = append(runErrs, fmt.Errorf("run %s: %w", run.ID, err))
			continue
		}
		processed++
	}
	return processed, runErrs
}

func (s *Service) processRun(ctx context.Context, tenantID string, run domain.InvoiceDunningRun) error {
	policy, err := s.store.GetPolicy(ctx, tenantID)
	if err != nil {
		return err
	}

	// Check for customer-specific dunning override
	if run.CustomerID != "" {
		if override, err := s.store.GetCustomerOverride(ctx, tenantID, run.CustomerID); err == nil {
			if override.MaxRetryAttempts != nil {
				policy.MaxRetryAttempts = *override.MaxRetryAttempts
			}
			if override.GracePeriodDays != nil {
				policy.GracePeriodDays = *override.GracePeriodDays
			}
			if override.FinalAction != "" {
				policy.FinalAction = domain.DunningFinalAction(override.FinalAction)
			}
		}
	}

	if run.Paused {
		return nil // Skip paused runs
	}

	// Check if max retries exhausted
	if run.AttemptCount >= policy.MaxRetryAttempts {
		return s.exhaustRun(ctx, tenantID, run, policy, s.clock.Now(ctx))
	}

	// Attempt retry
	run.AttemptCount++
	ctx = s.bindForInvoice(ctx, tenantID, run.InvoiceID)
	// Anchor this attempt on the simulated moment it was scheduled
	// for (run.NextActionAt) rather than the orchestrator's
	// frozen_time. Without this, every retry under catchup gets
	// last_attempt_at = advance-end frozen_time, and the next retry
	// is scheduled at frozen_time + interval (always past advance-end,
	// so it never fires in the same Advance click). Anchoring on
	// NextActionAt walks the state machine through simulated time:
	// retry 1 at NextActionAt (May 4), schedules retry 2 at May 7,
	// etc. Falls back to clock.Now() for runs missing NextActionAt
	// (defensive — shouldn't happen for runs that just passed
	// `next_action_at <= frozen_time`).
	now := s.clock.Now(ctx)
	if run.NextActionAt != nil {
		now = *run.NextActionAt
	}
	run.LastAttemptAt = &now

	// Actually retry the payment
	retryErr := fmt.Errorf("payment retrier not configured")
	if s.retrier != nil {
		retryErr = s.retrier.RetryPayment(ctx, tenantID, run.InvoiceID, run.CustomerID)
	}

	// Transient skip: the Stripe call never happened (circuit breaker open or
	// timeout before the call). Rewind the attempt count, leave state
	// untouched in DB, and let the next scheduler tick retry. This is NOT a
	// dunning attempt — do not tick attempt_count, do not log a failure
	// event, do not reschedule. A five-minute Stripe outage should not burn
	// a tenant's entire retry budget.
	if errors.Is(retryErr, ErrTransientSkip) {
		run.AttemptCount--
		slog.Info("dunning retry skipped — upstream transient (breaker/timeout)",
			"run_id", run.ID, "invoice_id", run.InvoiceID)
		return nil
	}

	if retryErr != nil {
		run.State = domain.DunningActive // Will retry again later
		slog.Warn("dunning retry failed",
			"run_id", run.ID,
			"invoice_id", run.InvoiceID,
			"attempt", run.AttemptCount,
			"error", retryErr,
		)

		// Record failed retry event at this retry's simulated instant
		// (= run.NextActionAt at fire time, captured into `now` above)
		// so each retry row on the invoice timeline carries its own
		// scheduled timestamp instead of all sharing frozen_time.
		_, _ = s.store.CreateEvent(ctx, tenantID, domain.InvoiceDunningEvent{
			RunID:        run.ID,
			InvoiceID:    run.InvoiceID,
			EventType:    domain.DunningEventRetryAttempted,
			State:        domain.DunningActive,
			AttemptCount: run.AttemptCount,
			Reason:       retryErr.Error(),
			CreatedAt:    now,
		})

		// Send dunning warning email asynchronously.
		//
		// Skip when THIS attempt has just used the last retry —
		// `exhaustRun` (called below at the post-attempt check, line
		// ~432) will fire the terminal escalation email instead. A
		// "we'll retry on [no more retries scheduled]" warning is
		// confusing and stacks two back-to-back emails on the customer.
		// Customer experience after fix: N-1 warnings during retries
		// 1..N-1, then ONE escalation on retry N. Catchup-mode
		// experience is the same, just compressed in real time.
		willExhaustThisAttempt := run.AttemptCount >= policy.MaxRetryAttempts
		if !willExhaustThisAttempt && s.emailNotifier != nil && s.customerEmail != nil {
			// Synchronous enqueue (DB insert via the email outbox).
			// Pre-fix this ran in a goroutine bound to the parent ctx,
			// which under test-clock catchup gets canceled the instant
			// RunCatchup returns (testclock/catchup.go:139's
			// `defer cancel()`). Goroutines spawned at the tail of
			// the catchup pass — the escalation in particular — lost
			// the race and never enqueued, even though the dunning
			// state was correctly transitioned. Synchronous enqueue
			// is fast (single INSERT); the SMTP send remains async
			// via the email outbox dispatcher worker.
			s.enqueueDunningWarning(ctx, tenantID, run, policy, retryErr.Error())
		}
	} else {
		run.State = domain.DunningResolved
		run.Resolution = domain.ResolutionPaymentRecovered
		run.ResolvedAt = &now
		run.NextActionAt = nil

		_, _ = s.store.CreateEvent(ctx, tenantID, domain.InvoiceDunningEvent{
			RunID:        run.ID,
			InvoiceID:    run.InvoiceID,
			EventType:    domain.DunningEventResolved,
			State:        domain.DunningResolved,
			AttemptCount: run.AttemptCount,
			Reason:       "payment_recovered",
			CreatedAt:    now,
		})

		slog.Info("dunning resolved — payment succeeded",
			"run_id", run.ID,
			"invoice_id", run.InvoiceID,
			"attempt", run.AttemptCount,
		)

		if _, err := s.store.UpdateRun(ctx, tenantID, run); err != nil {
			return err
		}
		return nil
	}

	// Schedule next retry.
	// retry_schedule contains the intervals between retries:
	//   retry_schedule[0] = gap between retry 1 and retry 2
	//   retry_schedule[1] = gap between retry 2 and retry 3
	// Grace period (used in StartDunning) determines when retry 1 happens.
	if run.AttemptCount < policy.MaxRetryAttempts {
		// Default intervals between retries: 3 days, 5 days, 7 days
		defaultIntervals := []time.Duration{72 * time.Hour, 120 * time.Hour, 168 * time.Hour}
		retryIntervals := defaultIntervals
		if len(policy.RetrySchedule) > 0 {
			retryIntervals = nil
			for _, s := range policy.RetrySchedule {
				if d, err := time.ParseDuration(s); err == nil {
					retryIntervals = append(retryIntervals, d)
				}
			}
			if len(retryIntervals) == 0 {
				retryIntervals = defaultIntervals
			}
		}
		// AttemptCount is 1-based; schedule[0] is the gap after retry 1
		idx := run.AttemptCount - 1
		if idx >= len(retryIntervals) {
			idx = len(retryIntervals) - 1 // reuse last interval
		}
		if idx >= 0 {
			t := now.Add(retryIntervals[idx])
			run.NextActionAt = &t
		}
	} else {
		run.NextActionAt = nil
	}

	if _, err := s.store.UpdateRun(ctx, tenantID, run); err != nil {
		return err
	}

	// Check if exhausted after this attempt. Pass the simulated instant
	// of this final retry (= run.NextActionAt at fire time, captured
	// into `now` above) so the escalated event row aligns with the
	// retry that actually triggered the exhaustion, not orchestrator
	// frozen_time.
	if run.AttemptCount >= policy.MaxRetryAttempts {
		return s.exhaustRun(ctx, tenantID, run, policy, now)
	}

	return nil
}

// exhaustRun finalizes a dunning run after its last retry failed (or
// it was found already at-or-beyond max attempts on entry). firedAt
// is the simulated instant of the triggering retry — used as the
// run's resolved_at and the escalated event's CreatedAt so the
// invoice timeline shows the escalation aligned with the retry that
// actually caused it, not at orchestrator frozen_time.
func (s *Service) exhaustRun(ctx context.Context, tenantID string, run domain.InvoiceDunningRun, policy domain.DunningPolicy, firedAt time.Time) error {
	ctx = s.bindForInvoice(ctx, tenantID, run.InvoiceID)
	now := firedAt
	if now.IsZero() {
		now = s.clock.Now(ctx)
	}
	run.State = domain.DunningEscalated
	run.Resolution = domain.ResolutionRetriesExhausted
	run.ResolvedAt = &now
	run.NextActionAt = nil

	switch policy.FinalAction {
	case domain.DunningActionManualReview:
		// resolution already set
	case domain.DunningActionPause:
		// Actually pause the subscription
		if s.subPauser != nil && s.invoiceGet != nil {
			if inv, err := s.invoiceGet.Get(ctx, tenantID, run.InvoiceID); err == nil && inv.SubscriptionID != "" {
				if err := s.subPauser.Pause(ctx, tenantID, inv.SubscriptionID); err != nil {
					slog.Warn("failed to pause subscription after dunning exhausted",
						"invoice_id", run.InvoiceID, "subscription_id", inv.SubscriptionID, "error", err)
				} else {
					slog.Info("subscription paused by dunning",
						"invoice_id", run.InvoiceID, "subscription_id", inv.SubscriptionID)
				}
			}
		}
	default:
		// write_off_later or unknown — resolution stays retries_exhausted
	}

	_, _ = s.store.CreateEvent(ctx, tenantID, domain.InvoiceDunningEvent{
		RunID:        run.ID,
		InvoiceID:    run.InvoiceID,
		EventType:    domain.DunningEventEscalated,
		State:        run.State,
		AttemptCount: run.AttemptCount,
		Reason:       string(policy.FinalAction),
		CreatedAt:    now,
	})

	if _, err := s.store.UpdateRun(ctx, tenantID, run); err != nil {
		return err
	}

	slog.Info("dunning exhausted",
		"run_id", run.ID,
		"invoice_id", run.InvoiceID,
		"final_action", policy.FinalAction,
	)

	// Synchronous enqueue — see comment on enqueueDunningWarning.
	if s.emailNotifier != nil && s.customerEmail != nil {
		s.enqueueDunningEscalation(ctx, tenantID, run, policy)
	}

	s.fireEvent(ctx, tenantID, domain.EventDunningEscalated, map[string]any{
		"run_id":       run.ID,
		"invoice_id":   run.InvoiceID,
		"customer_id":  run.CustomerID,
		"final_action": string(policy.FinalAction),
		"resolution":   string(run.Resolution),
		"attempts":     run.AttemptCount,
	})

	return nil
}

// ResolveRun marks a dunning run as resolved (e.g., after manual payment).
func (s *Service) ResolveRun(ctx context.Context, tenantID, runID string, resolution domain.DunningResolution) (domain.InvoiceDunningRun, error) {
	run, err := s.store.GetRun(ctx, tenantID, runID)
	if err != nil {
		return domain.InvoiceDunningRun{}, err
	}

	ctx = s.bindForInvoice(ctx, tenantID, run.InvoiceID)
	now := s.clock.Now(ctx)
	run.State = domain.DunningResolved
	run.Resolution = resolution
	run.ResolvedAt = &now
	run.NextActionAt = nil

	_, _ = s.store.CreateEvent(ctx, tenantID, domain.InvoiceDunningEvent{
		RunID:     run.ID,
		InvoiceID: run.InvoiceID,
		EventType: domain.DunningEventResolved,
		State:     domain.DunningResolved,
		Reason:    string(resolution),
		CreatedAt: now,
	})

	return s.store.UpdateRun(ctx, tenantID, run)
}

// ResolveByInvoice resolves any active dunning run for the given invoice.
// Called when an invoice is voided or paid outside of dunning.
func (s *Service) ResolveByInvoice(ctx context.Context, tenantID, invoiceID string, resolution domain.DunningResolution) error {
	run, err := s.store.GetActiveRunByInvoice(ctx, tenantID, invoiceID)
	if err != nil {
		return nil // No active run — nothing to resolve
	}

	ctx = s.bindForInvoice(ctx, tenantID, invoiceID)
	now := s.clock.Now(ctx)
	run.State = domain.DunningResolved
	run.Resolution = resolution
	run.ResolvedAt = &now
	run.NextActionAt = nil

	_, _ = s.store.CreateEvent(ctx, tenantID, domain.InvoiceDunningEvent{
		RunID:     run.ID,
		InvoiceID: run.InvoiceID,
		EventType: domain.DunningEventResolved,
		State:     domain.DunningResolved,
		Reason:    fmt.Sprintf("invoice %s", string(resolution)),
		CreatedAt: now,
	})

	_, err = s.store.UpdateRun(ctx, tenantID, run)
	return err
}

// GetPolicy returns the dunning policy for a tenant.
func (s *Service) GetPolicy(ctx context.Context, tenantID string) (domain.DunningPolicy, error) {
	return s.store.GetPolicy(ctx, tenantID)
}

// UpsertPolicy creates or updates the dunning policy for a tenant.
func (s *Service) UpsertPolicy(ctx context.Context, tenantID string, policy domain.DunningPolicy) (domain.DunningPolicy, error) {
	if policy.MaxRetryAttempts <= 0 {
		policy.MaxRetryAttempts = 3
	}
	if policy.MaxRetryAttempts > 15 {
		return domain.DunningPolicy{}, errs.Invalid("max_retry_attempts", "cannot exceed 15")
	}
	if policy.GracePeriodDays <= 0 {
		policy.GracePeriodDays = 3
	}
	if policy.GracePeriodDays > 30 {
		return domain.DunningPolicy{}, errs.Invalid("grace_period_days", "cannot exceed 30")
	}
	switch policy.FinalAction {
	case domain.DunningActionManualReview, domain.DunningActionPause, domain.DunningActionWriteOff:
		// valid
	case "":
		// Default = pause (matches DB column default in migration
		// 0071). Without this, the engine would keep generating
		// finalized invoices for delinquent customers every cycle —
		// stacking failures + dunning emails. pause + keep_as_draft
		// is the operator-protective choice. Operators who want a
		// different terminal action set it explicitly.
		policy.FinalAction = domain.DunningActionPause
	default:
		return domain.DunningPolicy{}, errs.Invalid("final_action", "must be one of: manual_review, pause, write_off_later")
	}
	if err := domain.MaxLen("name", policy.Name, 255); err != nil {
		return domain.DunningPolicy{}, err
	}
	return s.store.UpsertPolicy(ctx, tenantID, policy)
}

// UpsertPolicyTx forwards to the store's tx-aware upsert. Validation is
// skipped here because the recipe template layer already validated against
// the recipe schema. See pricing.Service tx variants for the same rationale.
func (s *Service) UpsertPolicyTx(ctx context.Context, tx *sql.Tx, tenantID string, policy domain.DunningPolicy) (domain.DunningPolicy, error) {
	return s.store.UpsertPolicyTx(ctx, tx, tenantID, policy)
}

// GetCustomerOverride returns the dunning override for a specific customer.
func (s *Service) GetCustomerOverride(ctx context.Context, tenantID, customerID string) (domain.CustomerDunningOverride, error) {
	return s.store.GetCustomerOverride(ctx, tenantID, customerID)
}

// UpsertCustomerOverride creates or updates a customer-level dunning override.
func (s *Service) UpsertCustomerOverride(ctx context.Context, tenantID string, override domain.CustomerDunningOverride) (domain.CustomerDunningOverride, error) {
	if override.CustomerID == "" {
		return domain.CustomerDunningOverride{}, errs.Required("customer_id")
	}
	if override.MaxRetryAttempts != nil && *override.MaxRetryAttempts > 15 {
		return domain.CustomerDunningOverride{}, errs.Invalid("max_retry_attempts", "cannot exceed 15")
	}
	if override.GracePeriodDays != nil && *override.GracePeriodDays > 30 {
		return domain.CustomerDunningOverride{}, errs.Invalid("grace_period_days", "cannot exceed 30")
	}
	if override.FinalAction != "" {
		switch domain.DunningFinalAction(override.FinalAction) {
		case domain.DunningActionManualReview, domain.DunningActionPause, domain.DunningActionWriteOff:
			// valid
		default:
			return domain.CustomerDunningOverride{}, errs.Invalid("final_action", "must be one of: manual_review, pause, write_off_later")
		}
	}
	return s.store.UpsertCustomerOverride(ctx, tenantID, override)
}

// DeleteCustomerOverride removes a customer-level dunning override.
func (s *Service) DeleteCustomerOverride(ctx context.Context, tenantID, customerID string) error {
	return s.store.DeleteCustomerOverride(ctx, tenantID, customerID)
}

// ListRuns returns dunning runs matching the filter.
func (s *Service) ListRuns(ctx context.Context, filter RunListFilter) ([]domain.InvoiceDunningRun, int, error) {
	return s.store.ListRuns(ctx, filter)
}

// enqueueDunningWarning resolves the customer's email + invoice context
// and synchronously enqueues a dunning-warning email via the outbox.
// Synchronous on purpose — `SendDunningWarning` is a fast DB INSERT;
// running it in a goroutine bound to the catchup ctx would race against
// `defer cancel()` in testclock/catchup.go and silently drop the email.
// The actual SMTP send happens later via the outbox dispatcher worker
// on its own long-lived ctx. Errors are logged (best-effort); they do
// NOT roll back the dunning state transition.
func (s *Service) enqueueDunningWarning(ctx context.Context, tenantID string, run domain.InvoiceDunningRun, policy domain.DunningPolicy, retryErrMsg string) {
	email, name, err := s.customerEmail.GetCustomerEmail(ctx, tenantID, run.CustomerID)
	if err != nil || email == "" {
		slog.Warn("skip dunning warning email — cannot resolve customer email",
			"run_id", run.ID, "customer_id", run.CustomerID, "error", err)
		return
	}
	invoiceNumber := run.InvoiceID
	var publicToken string
	if s.invoiceGet != nil {
		if inv, err := s.invoiceGet.Get(ctx, tenantID, run.InvoiceID); err == nil {
			invoiceNumber = inv.InvoiceNumber
			publicToken = inv.PublicToken
		}
	}
	nextRetry := "TBD"
	if run.NextActionAt != nil {
		nextRetry = run.NextActionAt.Format("January 2, 2006")
	}
	if err := s.emailNotifier.SendDunningWarning(ctx, tenantID, email, name, invoiceNumber, run.AttemptCount, policy.MaxRetryAttempts, nextRetry, retryErrMsg, publicToken); err != nil {
		slog.Error("failed to enqueue dunning warning email",
			"run_id", run.ID, "email", email, "error", err)
	}
}

// enqueueDunningEscalation is the escalation-email counterpart to
// enqueueDunningWarning. Same synchronous-enqueue rationale.
func (s *Service) enqueueDunningEscalation(ctx context.Context, tenantID string, run domain.InvoiceDunningRun, policy domain.DunningPolicy) {
	email, name, err := s.customerEmail.GetCustomerEmail(ctx, tenantID, run.CustomerID)
	if err != nil || email == "" {
		slog.Warn("skip dunning escalation email — cannot resolve customer email",
			"run_id", run.ID, "customer_id", run.CustomerID, "error", err)
		return
	}
	invoiceNumber := run.InvoiceID
	var publicToken string
	if s.invoiceGet != nil {
		if inv, err := s.invoiceGet.Get(ctx, tenantID, run.InvoiceID); err == nil {
			invoiceNumber = inv.InvoiceNumber
			publicToken = inv.PublicToken
		}
	}
	if err := s.emailNotifier.SendDunningEscalation(ctx, tenantID, email, name, invoiceNumber, string(policy.FinalAction), publicToken); err != nil {
		slog.Error("failed to enqueue dunning escalation email",
			"run_id", run.ID, "email", email, "error", err)
	}
}
