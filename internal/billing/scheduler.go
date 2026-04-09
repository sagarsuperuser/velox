package billing

import (
	"context"
	"log/slog"
	"time"
)

// DunningProcessor processes due dunning runs.
type DunningProcessor interface {
	ProcessDueRuns(ctx context.Context, tenantID string, limit int) (int, []error)
}

// TenantLister lists all tenant IDs for background processing.
type TenantLister interface {
	ListTenantIDs(ctx context.Context) ([]string, error)
}

// Scheduler runs the billing cycle engine and dunning processor on a periodic interval.
type Scheduler struct {
	engine   *Engine
	dunning  DunningProcessor
	tenants  TenantLister
	interval time.Duration
	batch    int
}

func NewScheduler(engine *Engine, interval time.Duration, batch int, dunning DunningProcessor, tenants TenantLister) *Scheduler {
	if interval <= 0 {
		interval = 1 * time.Hour
	}
	if batch <= 0 {
		batch = 50
	}
	return &Scheduler{engine: engine, dunning: dunning, tenants: tenants, interval: interval, batch: batch}
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

	// 1. Billing cycle — generate invoices
	generated, errs := s.engine.RunCycle(ctx, s.batch)

	duration := time.Since(start)
	if len(errs) > 0 {
		slog.Error("billing cycle completed with errors",
			"generated", generated,
			"errors", len(errs),
			"duration_ms", duration.Milliseconds(),
		)
		for _, err := range errs {
			slog.Error("billing cycle error", "error", err)
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
			if processed > 0 {
				slog.Info("dunning runs processed", "tenant_id", tid, "processed", processed)
			}
		}
	}
}
