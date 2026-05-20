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

// SubscriptionPauser pauses collection on a subscription when dunning
// exhausts retries with final_action='pause' (ADR-036 amendment —
// semantics changed to match Stripe's `pause_collection.behavior=
// keep_as_draft`: cycle continues, drafts pile up, no charging /
// dunning until resumed). Pre-amendment this called PauseAtomic
// (hard pause, no further cycles); that was non-Stripe and silently
// skipped invoice generation for the affected periods.
type SubscriptionPauser interface {
	PauseCollection(ctx context.Context, tenantID, id string) error
}

// SubscriptionCanceler cancels a subscription when dunning exhausts
// retries with final_action='cancel_subscription' — Stripe's default
// terminal action; supported by 3 of 4 reference platforms (Stripe,
// Lago, Recurly) per the ADR-036 amendment research.
type SubscriptionCanceler interface {
	Cancel(ctx context.Context, tenantID, id string) error
}

// InvoiceUncollectibleMarker marks an invoice uncollectible when
// dunning exhausts retries with final_action='mark_uncollectible'.
// Stripe-standard terminal: "we won't try again; close out the
// receivable." Replaces the pre-amendment write_off_later semantics
// (the implementation was identical — invoice mutation, no sub
// state-change — but the name was non-standard).
type InvoiceUncollectibleMarker interface {
	MarkUncollectible(ctx context.Context, tenantID, invoiceID string) error
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

// CustomerPolicyReader returns a customer's assigned dunning_policy_id
// (empty string = no explicit assignment, fall back to tenant default).
// Implemented by *customer.Service so the dunning service can resolve
// the effective policy without importing the customer package.
type CustomerPolicyReader interface {
	GetDunningPolicyID(ctx context.Context, tenantID, customerID string) (string, error)
}

type Service struct {
	store            Store
	retrier          PaymentRetrier
	subPauser        SubscriptionPauser
	subCanceler      SubscriptionCanceler
	invoiceUncollect InvoiceUncollectibleMarker
	invoiceGet       InvoiceGetter
	events           domain.EventDispatcher
	emailNotifier    EmailNotifier
	customerEmail    CustomerEmailFetcher
	custPolicy       CustomerPolicyReader
	resolver         clock.Resolver
	clock            clock.Clock
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

// SetCustomerPolicyReader wires the customer→policy_id lookup used by
// GetEffectivePolicyForCustomer. Without it, every dunning operation
// falls back to the tenant default policy (per-customer assignment
// silently ignored). Production must wire this; narrow unit tests
// can leave it nil.
func (s *Service) SetCustomerPolicyReader(r CustomerPolicyReader) {
	s.custPolicy = r
}

// GetEffectivePolicyForCustomer returns the policy that governs this
// customer's next dunning run. Resolution order (ADR-036):
//  1. If the customer has an explicit dunning_policy_id, load that
//     policy by id.
//  2. Otherwise, the tenant's is_default=true policy.
//
// Lookup failures on step 1 (e.g. the assigned policy was deleted
// underneath the customer's pointer; the FK ON DELETE SET NULL safety
// net should prevent this but defensive coding) fall through to the
// default — a missing-policy customer continues to dun rather than
// erroring out at run time.
func (s *Service) GetEffectivePolicyForCustomer(ctx context.Context, tenantID, customerID string) (domain.DunningPolicy, error) {
	if s.custPolicy != nil && customerID != "" {
		if pid, err := s.custPolicy.GetDunningPolicyID(ctx, tenantID, customerID); err == nil && pid != "" {
			if p, err := s.store.GetPolicyByID(ctx, tenantID, pid); err == nil {
				return p, nil
			}
			// Assigned policy not found — fall through to default.
		}
	}
	return s.store.GetDefaultPolicy(ctx, tenantID)
}

// SetSubscriptionPauser configures the pause-collection terminal action
// (ADR-036 amendment — semantics now Stripe-aligned: keep_as_draft,
// not hard pause).
func (s *Service) SetSubscriptionPauser(pauser SubscriptionPauser, invoices InvoiceGetter) {
	s.subPauser = pauser
	s.invoiceGet = invoices
}

// SetSubscriptionCanceler configures the cancel-subscription terminal
// action (ADR-036 amendment).
func (s *Service) SetSubscriptionCanceler(c SubscriptionCanceler) {
	s.subCanceler = c
}

// SetInvoiceUncollectibleMarker configures the mark-uncollectible
// terminal action (ADR-036 amendment).
func (s *Service) SetInvoiceUncollectibleMarker(m InvoiceUncollectibleMarker) {
	s.invoiceUncollect = m
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

	policy, err := s.GetEffectivePolicyForCustomer(ctx, tenantID, customerID)
	if err != nil {
		return domain.InvoiceDunningRun{}, fmt.Errorf("get effective dunning policy: %w", err)
	}
	if !policy.Enabled {
		return domain.InvoiceDunningRun{}, errs.InvalidState("dunning is disabled (assigned policy or tenant default is not enabled)")
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
	// Resolve the policy bound to this run at StartDunning time.
	// Runs stay on their original policy for their lifetime — if the
	// customer's dunning_policy_id assignment changed mid-flight (or
	// the assigned policy was edited), in-flight runs continue with
	// the original config and only the NEXT run for this customer
	// picks up the new policy. Stripe-Lago shape (verified during
	// ADR-036 research — no platform switches a mid-flight retry
	// schedule under the operator's feet).
	policy, err := s.store.GetPolicyByID(ctx, tenantID, run.PolicyID)
	if err != nil {
		return fmt.Errorf("get bound policy %s for run %s: %w", run.PolicyID, run.ID, err)
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
	//
	// Save-time validation (Service.UpsertPolicy) guarantees the schedule
	// has at least (MaxRetryAttempts - 1) entries. The pre-ADR-036
	// "reuse last interval when idx out of bounds" runtime fallback was
	// removed — that was a silent fallback (feedback_no_silent_fallbacks);
	// out-of-bounds here now indicates a schema invariant violation, so
	// fail loudly rather than substitute a default.
	if run.AttemptCount < policy.MaxRetryAttempts {
		idx := run.AttemptCount - 1 // 1-based; schedule[0] = after retry 1
		if idx < 0 || idx >= len(policy.RetrySchedule) {
			return fmt.Errorf("retry_schedule index %d out of bounds for policy %s (schedule has %d entries, max_retry_attempts %d) — save-time validation must enforce schedule length",
				idx, policy.ID, len(policy.RetrySchedule), policy.MaxRetryAttempts)
		}
		d, err := time.ParseDuration(policy.RetrySchedule[idx])
		if err != nil {
			return fmt.Errorf("parse retry_schedule[%d]=%q for policy %s: %w", idx, policy.RetrySchedule[idx], policy.ID, err)
		}
		t := now.Add(d)
		run.NextActionAt = &t
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
		// State stays at escalated; resolution stays retries_exhausted.
		// Operator handles via the dashboard. Stripe "Keep active"
		// equivalent.

	case domain.DunningActionPause:
		// Pause COLLECTION (keep_as_draft) — cycle keeps drafting,
		// no charging / dunning until the operator resumes. Matches
		// Stripe's pause_collection.behavior=keep_as_draft. The pre-
		// ADR-036-amendment implementation called hard PauseAtomic,
		// which silently skipped invoice generation for the affected
		// periods — non-Stripe and destructive.
		if s.subPauser != nil && s.invoiceGet != nil {
			if inv, err := s.invoiceGet.Get(ctx, tenantID, run.InvoiceID); err == nil && inv.SubscriptionID != "" {
				if err := s.subPauser.PauseCollection(ctx, tenantID, inv.SubscriptionID); err != nil {
					slog.Warn("failed to pause collection after dunning exhausted",
						"invoice_id", run.InvoiceID, "subscription_id", inv.SubscriptionID, "error", err)
				} else {
					slog.Info("collection paused by dunning",
						"invoice_id", run.InvoiceID, "subscription_id", inv.SubscriptionID)
				}
			}
		}

	case domain.DunningActionCancelSubscription:
		// Cancel the subscription. Stripe-default terminal action;
		// supported by 3 of 4 reference platforms (Stripe, Lago,
		// Recurly) per the ADR-036 amendment research.
		if s.subCanceler != nil && s.invoiceGet != nil {
			if inv, err := s.invoiceGet.Get(ctx, tenantID, run.InvoiceID); err == nil && inv.SubscriptionID != "" {
				if err := s.subCanceler.Cancel(ctx, tenantID, inv.SubscriptionID); err != nil {
					slog.Warn("failed to cancel subscription after dunning exhausted",
						"invoice_id", run.InvoiceID, "subscription_id", inv.SubscriptionID, "error", err)
				} else {
					slog.Info("subscription canceled by dunning",
						"invoice_id", run.InvoiceID, "subscription_id", inv.SubscriptionID)
				}
			}
		}

	case domain.DunningActionMarkUncollectible:
		// Mark the unpaid invoice as uncollectible. Stripe-standard
		// for "we won't try again; close out the receivable." The
		// subscription itself stays active — operator may
		// independently cancel via the dashboard.
		if s.invoiceUncollect != nil {
			if err := s.invoiceUncollect.MarkUncollectible(ctx, tenantID, run.InvoiceID); err != nil {
				slog.Warn("failed to mark invoice uncollectible after dunning exhausted",
					"invoice_id", run.InvoiceID, "error", err)
			} else {
				slog.Info("invoice marked uncollectible by dunning",
					"invoice_id", run.InvoiceID)
			}
		}

	default:
		slog.Warn("unknown dunning final_action — leaving run escalated without state-change action",
			"final_action", policy.FinalAction, "run_id", run.ID)
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
//
// When resolution is invoice_not_collectible, the underlying invoice is
// ALSO flipped to status=uncollectible — same downstream contract as
// the automated mark_uncollectible final-action. Pre-fix the two flows
// diverged: an operator picking "Write off invoice" in the resolve
// dialog only updated the dunning_runs row, leaving the invoice in
// status=finalized + payment_status=failed and every UI gate keyed
// off invoice.status still treating it as live. Cross-flow audit per
// feedback_audit_overlapping_flows: same operator intent ("we're not
// collecting"), same end state required.
//
// The invoice-side write is best-effort and logged on failure rather
// than rolled back — the run's resolved state is itself useful for the
// dunning history view, and a wrapper transaction across two domains
// is heavier than the rarity warrants. If MarkUncollectible 4xxs
// (e.g. invoice was already paid via webhook between dialog open and
// submit), we surface the error to the caller and the operator can
// reconcile.
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

	updated, err := s.store.UpdateRun(ctx, tenantID, run)
	if err != nil {
		return domain.InvoiceDunningRun{}, err
	}

	if resolution == domain.ResolutionInvoiceNotCollectible && s.invoiceUncollect != nil {
		if err := s.invoiceUncollect.MarkUncollectible(ctx, tenantID, run.InvoiceID); err != nil {
			// Already-uncollectible is benign (race with automated
			// final_action). Surface other errors so the operator
			// knows the dunning run was resolved but the invoice
			// didn't transition.
			if !errors.Is(err, errs.ErrInvalidState) {
				return updated, fmt.Errorf("mark invoice uncollectible: %w", err)
			}
		}
	}

	return updated, nil
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

// GetDefaultPolicy returns the tenant's default dunning policy.
// Operators / handlers needing "the current policy" use this; runtime
// dunning state machine instead resolves per-customer via
// GetEffectivePolicyForCustomer or per-run via GetPolicyByID(run.PolicyID).
func (s *Service) GetDefaultPolicy(ctx context.Context, tenantID string) (domain.DunningPolicy, error) {
	return s.store.GetDefaultPolicy(ctx, tenantID)
}

// GetPolicyByID looks up a policy by id.
func (s *Service) GetPolicyByID(ctx context.Context, tenantID, id string) (domain.DunningPolicy, error) {
	return s.store.GetPolicyByID(ctx, tenantID, id)
}

// ListPolicies returns all policies for the tenant (default first, then
// created_at). Drives the campaigns admin page.
func (s *Service) ListPolicies(ctx context.Context, tenantID string) ([]domain.DunningPolicy, error) {
	return s.store.ListPolicies(ctx, tenantID)
}

// DeletePolicy removes a policy by id. Refuses to delete the default
// policy or any policy with customers explicitly assigned to it (the
// store-level checks are authoritative; service-level pre-check just
// produces a clearer error before hitting the DB).
func (s *Service) DeletePolicy(ctx context.Context, tenantID, id string) error {
	return s.store.DeletePolicy(ctx, tenantID, id)
}

// SetDefaultPolicy promotes a policy to is_default and demotes the
// previous default atomically.
func (s *Service) SetDefaultPolicy(ctx context.Context, tenantID, id string) error {
	return s.store.SetDefaultPolicy(ctx, tenantID, id)
}

// CountCustomersOnPolicy returns the explicit-assignment count for a
// policy, used by the admin UI ("N customers assigned" badge) and by
// the delete guard.
func (s *Service) CountCustomersOnPolicy(ctx context.Context, tenantID, policyID string) (int, error) {
	return s.store.CountCustomersOnPolicy(ctx, tenantID, policyID)
}

// UpsertPolicy creates a new policy (when input.ID is empty) or updates
// an existing one (when input.ID is set). Save-time validation enforces
// the per-platform invariant that retry_schedule has enough entries to
// cover the max-attempts count — without this guard a misconfig (max=5
// with 2-entry schedule) would silently reuse the last interval at
// runtime, producing back-to-back retries the operator never asked for
// (a feedback_no_silent_fallbacks violation; the pre-ADR-036 runtime
// fallback `idx >= len(retryIntervals)` was removed alongside this).
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
	case domain.DunningActionManualReview, domain.DunningActionPause,
		domain.DunningActionMarkUncollectible, domain.DunningActionCancelSubscription:
		// valid
	case "":
		// Default = pause (Stripe-aligned pause_collection.behavior=
		// keep_as_draft semantics after ADR-036 amendment). Without
		// this, the engine would keep generating finalized invoices
		// for delinquent customers every cycle — stacking failures +
		// dunning emails. pause + keep_as_draft is the operator-
		// protective choice. Operators who want a different terminal
		// action set it explicitly.
		policy.FinalAction = domain.DunningActionPause
	default:
		return domain.DunningPolicy{}, errs.Invalid("final_action",
			"must be one of: manual_review, pause, mark_uncollectible, cancel_subscription")
	}
	if err := domain.MaxLen("name", policy.Name, 255); err != nil {
		return domain.DunningPolicy{}, err
	}
	// retry_schedule length must support max_retry_attempts. Retry 1
	// uses grace_period_days; retries 2..N use retry_schedule[0..N-2].
	// So schedule must have at least MaxRetryAttempts - 1 entries.
	needed := policy.MaxRetryAttempts - 1
	if needed > 0 && len(policy.RetrySchedule) < needed {
		return domain.DunningPolicy{}, errs.Invalid("retry_schedule",
			fmt.Sprintf("max_retry_attempts (%d) requires at least %d retry_schedule entries — got %d",
				policy.MaxRetryAttempts, needed, len(policy.RetrySchedule)))
	}
	return s.store.UpsertPolicy(ctx, tenantID, policy)
}

// UpsertPolicyTx forwards to the store's tx-aware upsert. Validation is
// skipped here because the recipe template layer already validated against
// the recipe schema. See pricing.Service tx variants for the same rationale.
func (s *Service) UpsertPolicyTx(ctx context.Context, tx *sql.Tx, tenantID string, policy domain.DunningPolicy) (domain.DunningPolicy, error) {
	return s.store.UpsertPolicyTx(ctx, tx, tenantID, policy)
}

// ListRuns returns dunning runs matching the filter.
func (s *Service) ListRuns(ctx context.Context, filter RunListFilter) ([]domain.InvoiceDunningRun, int, error) {
	return s.store.ListRuns(ctx, filter)
}

func (s *Service) GetStats(ctx context.Context, tenantID string) (Stats, error) {
	return s.store.GetStats(ctx, tenantID)
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
