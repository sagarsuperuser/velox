package billing

import (
	"context"
	"log/slog"
	"time"
)

// Scheduler runs the billing cycle engine on a periodic interval.
// It's a simple background goroutine — no Temporal, no cron library.
// For a billing platform, predictable timing matters more than fancy scheduling.
type Scheduler struct {
	engine   *Engine
	interval time.Duration
	batch    int
}

func NewScheduler(engine *Engine, interval time.Duration, batch int) *Scheduler {
	if interval <= 0 {
		interval = 1 * time.Hour
	}
	if batch <= 0 {
		batch = 50
	}
	return &Scheduler{engine: engine, interval: interval, batch: batch}
}

// Start runs the scheduler in a background goroutine.
// It blocks until the context is canceled (graceful shutdown).
func (s *Scheduler) Start(ctx context.Context) {
	slog.Info("billing scheduler started",
		"interval", s.interval.String(),
		"batch_size", s.batch,
	)

	// Run immediately on start
	s.runOnce(ctx)

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
	// If generated == 0 and no errors, stay silent — nothing to do
}
