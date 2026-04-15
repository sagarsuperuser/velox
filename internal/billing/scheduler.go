package billing

import (
	"context"
	"log/slog"
	"time"

	mw "github.com/sagarsuperuser/velox/internal/api/middleware"
	"github.com/sagarsuperuser/velox/internal/domain"
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

// Scheduler runs the billing cycle engine and dunning processor on a periodic interval.
type Scheduler struct {
	engine       *Engine
	dunning      DunningProcessor
	tenants      TenantLister
	credits      CreditExpirer
	reminders    InvoiceReminder
	tokenCleaner TokenCleaner
	interval     time.Duration
	batch        int
	onRun        func() // called after each complete scheduler tick (for health tracking)
}

// Interval returns the configured scheduler interval.
func (s *Scheduler) Interval() time.Duration { return s.interval }

func NewScheduler(engine *Engine, interval time.Duration, batch int, dunning DunningProcessor, tenants TenantLister, credits ...CreditExpirer) *Scheduler {
	if interval <= 0 {
		interval = 1 * time.Hour
	}
	if batch <= 0 {
		batch = 50
	}
	s := &Scheduler{engine: engine, dunning: dunning, tenants: tenants, interval: interval, batch: batch}
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
	start := time.Now()

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

	// 2. Dunning — process due retry runs
	if s.dunning != nil && s.tenants != nil {
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
		}
	}

	// Notify health check that a scheduler tick completed
	if s.onRun != nil {
		s.onRun()
	}
}
