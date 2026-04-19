package billing

import (
	"context"
	"log/slog"
	"time"

	mw "github.com/sagarsuperuser/velox/internal/api/middleware"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/platform/clock"
)

// DunningProcessor processes due dunning runs.
type DunningProcessor interface {
	ProcessDueRuns(ctx context.Context, tenantID string, limit int) (int, []error)
}

// TenantLister lists all tenant IDs for background processing.
type TenantLister interface {
	ListTenantIDs(ctx context.Context) ([]string, error)
}

// CreditExpirer expires credit grants past their expiry date.
type CreditExpirer interface {
	ExpireCredits(ctx context.Context) (int, []error)
}

// InvoiceReminder queries invoices approaching their due date.
type InvoiceReminder interface {
	ListApproachingDue(ctx context.Context, daysBeforeDue int) ([]domain.Invoice, error)
}

// TokenCleaner cleans up expired payment update tokens.
type TokenCleaner interface {
	Cleanup(ctx context.Context) (int, error)
}

// IdempotencyCleaner cleans up expired idempotency keys.
type IdempotencyCleaner interface {
	Cleanup(ctx context.Context) (int, error)
}

// PaymentReconciler resolves invoices in the PaymentUnknown state by querying
// Stripe for the authoritative PaymentIntent outcome. See payment.Reconciler.
type PaymentReconciler interface {
	Run(ctx context.Context, limit int) (int, []error)
}

// Lock represents a held cluster-wide lock that the holder must release.
type Lock interface {
	Release()
}

// Locker acquires cluster-wide singleton locks by key — typically backed by
// Postgres advisory locks. Returned (nil, false, nil) means another leader
// holds the lock; caller should skip the tick. Nil Locker disables leader
// gating (single-replica mode).
type Locker interface {
	TryAdvisoryLock(ctx context.Context, key int64) (Lock, bool, error)
}

// Scheduler runs the billing cycle engine and dunning processor on a periodic interval.
type Scheduler struct {
	engine            *Engine
	dunning           DunningProcessor
	tenants           TenantLister
	credits           CreditExpirer
	reminders         InvoiceReminder
	tokenCleaner      TokenCleaner
	idempotencyClean  IdempotencyCleaner
	paymentReconciler PaymentReconciler
	locker            Locker
	billingLockKey    int64
	dunningLockKey    int64
	interval          time.Duration
	batch             int
	onRun             func() // called after each complete scheduler tick (for health tracking)
	clock             clock.Clock
}

// Interval returns the configured scheduler interval.
func (s *Scheduler) Interval() time.Duration { return s.interval }

func NewScheduler(engine *Engine, interval time.Duration, batch int, dunning DunningProcessor, tenants TenantLister, clk clock.Clock, credits ...CreditExpirer) *Scheduler {
	if interval <= 0 {
		interval = 1 * time.Hour
	}
	if batch <= 0 {
		batch = 50
	}
	if clk == nil {
		clk = clock.Real()
	}
	s := &Scheduler{engine: engine, dunning: dunning, tenants: tenants, interval: interval, batch: batch, clock: clk}
	if len(credits) > 0 {
		s.credits = credits[0]
	}
	return s
}

// SetReminders sets the invoice reminder dependency for due-date notifications.
func (s *Scheduler) SetReminders(reminders InvoiceReminder) {
	s.reminders = reminders
}

// SetTokenCleaner sets the token cleanup dependency for expired payment update tokens.
func (s *Scheduler) SetTokenCleaner(cleaner TokenCleaner) {
	s.tokenCleaner = cleaner
}

// SetIdempotencyCleaner wires the idempotency-key cleanup task. Without a
// periodic purge the idempotency_keys table grows unbounded (one row per
// mutating API call per tenant), and every cache lookup walks a larger
// B-tree — a slow leak that only shows up in p99 latency weeks later.
func (s *Scheduler) SetIdempotencyCleaner(cleaner IdempotencyCleaner) {
	s.idempotencyClean = cleaner
}

// SetPaymentReconciler wires a resolver for PaymentUnknown invoices. Runs
// each tick after auto-charge retries, before the billing cycle generates
// new invoices, so a charge stuck in the unknown state can clear before
// we risk issuing a duplicate.
func (s *Scheduler) SetPaymentReconciler(r PaymentReconciler) {
	s.paymentReconciler = r
}

// SetLocker enables leader gating. When set, the scheduler only runs the
// billing and dunning halves of its tick if it wins the relevant advisory
// lock — preventing two app replicas from both generating invoices or both
// advancing the same dunning run. Pass nil (default) for single-replica or
// test-mode operation where gating is unwanted.
func (s *Scheduler) SetLocker(locker Locker, billingKey, dunningKey int64) {
	s.locker = locker
	s.billingLockKey = billingKey
	s.dunningLockKey = dunningKey
}

// SetOnRun registers a callback invoked after each complete scheduler tick.
// Used by the API health check to track scheduler liveness.
func (s *Scheduler) SetOnRun(fn func()) {
	s.onRun = fn
}

// Start runs the scheduler in a background goroutine.
// It blocks until the context is canceled (graceful shutdown).
func (s *Scheduler) Start(ctx context.Context) {
	slog.Info("billing scheduler started",
		"interval", s.interval.String(),
		"batch_size", s.batch,
	)

	// Wait for first interval before running (don't bill on startup)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("billing scheduler stopped")
			return
		case <-ticker.C:
			s.runOnce(ctx)
		}
	}
}

func (s *Scheduler) runOnce(ctx context.Context) {
	s.runBillingHalf(ctx)
	s.runDunningHalf(ctx)

	// Notify health check that a scheduler tick completed. Fires even when
	// both halves were skipped (lock contention) so the health probe still
	// sees the scheduler as alive on follower replicas.
	if s.onRun != nil {
		s.onRun()
	}
}

// runBillingHalf runs the leader-gated half that generates money — invoice
// issuance, payment reconciliation, auto-charge retries, cleanup sweeps, and
// reminders. Splitting from dunning lets two replicas divvy up roles instead
// of one replica monopolising every periodic job.
func (s *Scheduler) runBillingHalf(ctx context.Context) {
	start := time.Now()

	if s.locker != nil {
		lock, acquired, err := s.locker.TryAdvisoryLock(ctx, s.billingLockKey)
		if err != nil {
			slog.Error("billing scheduler: lock acquire failed", "error", err)
			return
		}
		if !acquired {
			slog.Debug("billing scheduler: another leader holds the lock; skipping tick")
			return
		}
		defer lock.Release()
	}

	// 0a. Reconcile PaymentUnknown invoices against Stripe. Runs before
	// auto-charge retry so any stuck-unknown charge that actually succeeded
	// is marked paid before the retry path considers re-charging.
	if s.paymentReconciler != nil {
		resolved, rErrs := s.paymentReconciler.Run(ctx, s.batch)
		if resolved > 0 {
			slog.Info("payment reconciler resolved unknowns", "count", resolved)
		}
		for _, e := range rErrs {
			slog.Error("payment reconciler error", "error", e)
		}
	}

	// 0. Retry pending auto-charges from previous cycles
	if chargeRetried, chargeErrs := s.engine.RetryPendingCharges(ctx, s.batch); chargeRetried > 0 || len(chargeErrs) > 0 {
		slog.Info("auto-charge retries", "succeeded", chargeRetried, "errors", len(chargeErrs))
		for i := 0; i < chargeRetried; i++ {
			mw.RecordAutoChargeRetry("succeeded")
		}
		for _, e := range chargeErrs {
			slog.Error("auto-charge retry error", "error", e)
			mw.RecordAutoChargeRetry("failed")
		}
	}

	// 1. Billing cycle — generate invoices
	generated, errs := s.engine.RunCycle(ctx, s.batch)

	duration := time.Since(start)
	mw.RecordBillingCycleDuration(duration.Seconds())
	if len(errs) > 0 {
		slog.Error("billing cycle completed with errors",
			"generated", generated,
			"errors", len(errs),
			"duration_ms", duration.Milliseconds(),
		)
		for _, err := range errs {
			slog.Error("billing cycle error", "error", err)
			mw.RecordBillingCycleError()
		}
	} else if generated > 0 {
		slog.Info("billing cycle completed",
			"generated", generated,
			"duration_ms", duration.Milliseconds(),
		)
	}

	// 3. Credit expiry sweep
	if s.credits != nil {
		expired, cErrs := s.credits.ExpireCredits(ctx)
		if len(cErrs) > 0 {
			for _, e := range cErrs {
				slog.Error("credit expiry error", "error", e)
			}
		}
		for i := 0; i < expired; i++ {
			mw.RecordCreditOperation("expiry")
		}
		if expired > 0 {
			slog.Info("credits expired", "count", expired)
		}
	}

	// 4. Invoice payment reminders (3 days before due)
	if s.reminders != nil {
		approaching, err := s.reminders.ListApproachingDue(ctx, 3)
		if err != nil {
			slog.Error("approaching due query failed", "error", err)
		} else if len(approaching) > 0 {
			slog.Info("invoices approaching due date", "count", len(approaching))
		}
	}

	// 5. Payment update token cleanup (expired > 7 days)
	if s.tokenCleaner != nil {
		cleaned, err := s.tokenCleaner.Cleanup(ctx)
		if err != nil {
			slog.Error("token cleanup error", "error", err)
		} else if cleaned > 0 {
			slog.Info("expired payment tokens cleaned up", "count", cleaned)
			mw.RecordScheduledCleanup("payment_tokens", cleaned)
		}
	}

	// 6. Idempotency key cleanup (expires_at < now, default 24h retention).
	// Prevents unbounded growth of idempotency_keys, which would slow every
	// cache lookup as the B-tree deepens.
	if s.idempotencyClean != nil {
		cleaned, err := s.idempotencyClean.Cleanup(ctx)
		if err != nil {
			slog.Error("idempotency cleanup error", "error", err)
		} else if cleaned > 0 {
			slog.Info("expired idempotency keys cleaned up", "count", cleaned)
			mw.RecordScheduledCleanup("idempotency_keys", cleaned)
		}
	}
}

// runDunningHalf runs the leader-gated half that advances dunning state.
// Held behind a separate lock key so a replica can win dunning even if
// another replica is currently running the (longer-lived) billing half.
func (s *Scheduler) runDunningHalf(ctx context.Context) {
	if s.dunning == nil || s.tenants == nil {
		return
	}

	if s.locker != nil {
		lock, acquired, err := s.locker.TryAdvisoryLock(ctx, s.dunningLockKey)
		if err != nil {
			slog.Error("dunning scheduler: lock acquire failed", "error", err)
			return
		}
		if !acquired {
			slog.Debug("dunning scheduler: another leader holds the lock; skipping tick")
			return
		}
		defer lock.Release()
	}

	tenantIDs, err := s.tenants.ListTenantIDs(ctx)
	if err != nil {
		slog.Error("dunning: failed to list tenants", "error", err)
		return
	}
	for _, tid := range tenantIDs {
		processed, dErrs := s.dunning.ProcessDueRuns(ctx, tid, 20)
		if len(dErrs) > 0 {
			for _, e := range dErrs {
				slog.Error("dunning error", "tenant_id", tid, "error", e)
			}
		}
		for i := 0; i < processed; i++ {
			mw.RecordDunningRun()
		}
		if processed > 0 {
			slog.Info("dunning runs processed", "tenant_id", tid, "processed", processed)
		}
	}
}
