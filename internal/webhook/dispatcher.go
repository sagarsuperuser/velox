package webhook

import (
	"context"
	"log/slog"
	"time"
)

// DispatcherConfig controls the outbox dispatcher loop.
type DispatcherConfig struct {
	// Interval is the poll cadence between ProcessBatch calls. Default 2s if zero.
	Interval time.Duration
	// BatchSize bounds how many rows are claimed per tick. Default 25 if zero.
	BatchSize int
	// BatchTimeout bounds how long a single batch is allowed to run before its
	// tx is cancelled (releasing row locks). Default 30s if zero.
	BatchTimeout time.Duration
}

// Dispatcher drains the webhook_outbox by invoking Service.Dispatch for each
// pending row. It is the bridge between the durable outbox (what producers
// enqueue) and the existing per-endpoint delivery pipeline (webhook_events +
// webhook_deliveries). Handler semantics: a row is marked 'dispatched' once
// Service.Dispatch returns nil, which means the event has been persisted and
// queued to all matching endpoints — per-endpoint HTTP retry is then owned
// by Service.StartRetryWorker, independent of the outbox.
type Dispatcher struct {
	outbox *OutboxStore
	svc    *Service
	cfg    DispatcherConfig
}

func NewDispatcher(outbox *OutboxStore, svc *Service, cfg DispatcherConfig) *Dispatcher {
	if cfg.Interval <= 0 {
		cfg.Interval = 2 * time.Second
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 25
	}
	if cfg.BatchTimeout <= 0 {
		cfg.BatchTimeout = 30 * time.Second
	}
	return &Dispatcher{outbox: outbox, svc: svc, cfg: cfg}
}

// Start runs the dispatcher loop until ctx is cancelled. Intended to be
// launched as a goroutine from cmd/velox during boot, alongside the existing
// webhook retry worker.
func (d *Dispatcher) Start(ctx context.Context) {
	ticker := time.NewTicker(d.cfg.Interval)
	defer ticker.Stop()

	slog.Info("webhook outbox dispatcher started",
		"interval", d.cfg.Interval.String(),
		"batch_size", d.cfg.BatchSize,
	)

	for {
		select {
		case <-ctx.Done():
			slog.Info("webhook outbox dispatcher stopped")
			return
		case <-ticker.C:
			d.tick(ctx)
		}
	}
}

// tick drains one batch. Errors are logged and swallowed — the next tick will
// retry. A per-tick timeout ensures a stuck handler can't hold row locks
// indefinitely if the dispatcher ctx is long-lived.
func (d *Dispatcher) tick(ctx context.Context) {
	batchCtx, cancel := context.WithTimeout(ctx, d.cfg.BatchTimeout)
	defer cancel()

	n, err := d.outbox.ProcessBatch(batchCtx, d.cfg.BatchSize, d.handle)
	if err != nil {
		slog.Error("webhook outbox dispatcher: batch error", "error", err)
		return
	}
	if n > 0 {
		slog.Debug("webhook outbox dispatcher: batch processed", "count", n)
	}
}

// handle is the per-row handler. Returning nil marks the row 'dispatched';
// returning an error schedules a retry (or DLQ after MaxOutboxAttempts).
func (d *Dispatcher) handle(ctx context.Context, row OutboxRow) error {
	return d.svc.Dispatch(ctx, row.TenantID, row.EventType, row.Payload)
}
