package email

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

// DispatcherConfig controls the outbox dispatcher loop.
type DispatcherConfig struct {
	// Interval is the poll cadence between ProcessBatch calls. Default 5s if zero.
	// A little slower than webhooks (2s) because SMTP latency + provider rate
	// limits make sub-second reactions pointless for email.
	Interval time.Duration
	// BatchSize bounds how many rows are claimed per tick. Default 10 if zero —
	// kept small because each handler makes a real SMTP round-trip and we don't
	// want one batch to monopolise a connection slot for a minute.
	BatchSize int
	// BatchTimeout bounds how long a single batch is allowed to run before its
	// tx is cancelled (releasing row locks). Default 60s if zero.
	BatchTimeout time.Duration
}

// DispatchLock is a held cluster-wide lock the dispatcher must release.
type DispatchLock interface {
	Release()
}

// DispatchLocker gates the dispatcher tick on a cluster-wide advisory lock.
// Row-level FOR UPDATE SKIP LOCKED already prevents double-delivery when two
// dispatchers race, but the lock avoids both replicas issuing the same claim
// query every tick when only one drain worker is actually needed — less churn
// on the connection pool and on email_outbox's index scan. Nil Locker
// disables gating (single-replica / test mode).
type DispatchLocker interface {
	TryDispatcherLock(ctx context.Context) (DispatchLock, bool, error)
}

// EmailDeliverer is the SMTP-sending narrow interface the dispatcher calls.
// Satisfied by *email.Sender. Defined here so tests can inject fakes without
// touching the production Sender.
type EmailDeliverer interface {
	SendInvoice(tenantID, to, customerName, invoiceNumber string, totalCents int64, currency string, pdfBytes []byte) error
	SendPaymentReceipt(tenantID, to, customerName, invoiceNumber string, amountCents int64, currency string) error
	SendDunningWarning(tenantID, to, customerName, invoiceNumber string, attemptNumber, maxAttempts int, nextRetryDate string) error
	SendDunningEscalation(tenantID, to, customerName, invoiceNumber string, action string) error
	SendPaymentFailed(tenantID, to, customerName, invoiceNumber, reason string) error
	SendPaymentUpdateRequest(tenantID, to, customerName, invoiceNumber string, amountDueCents int64, currency, updateURL string) error
	SendPortalMagicLink(tenantID, to, customerName, magicLinkURL string) error
}

// Dispatcher drains the email_outbox by deserialising each row's payload and
// invoking the matching Send* method on the wrapped EmailDeliverer (the real
// SMTP Sender in production). Handler semantics: a row is marked 'dispatched'
// once the Send* call returns nil.
type Dispatcher struct {
	outbox *OutboxStore
	sender EmailDeliverer
	cfg    DispatcherConfig
	locker DispatchLocker
}

func NewDispatcher(outbox *OutboxStore, sender EmailDeliverer, cfg DispatcherConfig) *Dispatcher {
	if cfg.Interval <= 0 {
		cfg.Interval = 5 * time.Second
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 10
	}
	if cfg.BatchTimeout <= 0 {
		cfg.BatchTimeout = 60 * time.Second
	}
	return &Dispatcher{outbox: outbox, sender: sender, cfg: cfg}
}

// SetLocker enables leader gating on the dispatcher tick.
func (d *Dispatcher) SetLocker(locker DispatchLocker) {
	d.locker = locker
}

// Start runs the dispatcher loop until ctx is cancelled. Intended to be
// launched as a goroutine from cmd/velox during boot.
func (d *Dispatcher) Start(ctx context.Context) {
	ticker := time.NewTicker(d.cfg.Interval)
	defer ticker.Stop()

	slog.Info("email outbox dispatcher started",
		"interval", d.cfg.Interval.String(),
		"batch_size", d.cfg.BatchSize,
	)

	for {
		select {
		case <-ctx.Done():
			slog.Info("email outbox dispatcher stopped")
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

	if d.locker != nil {
		lock, acquired, err := d.locker.TryDispatcherLock(batchCtx)
		if err != nil {
			slog.Error("email outbox dispatcher: lock acquire failed", "error", err)
			return
		}
		if !acquired {
			return
		}
		defer lock.Release()
	}

	n, err := d.outbox.ProcessBatch(batchCtx, d.cfg.BatchSize, d.handle)
	if err != nil {
		slog.Error("email outbox dispatcher: batch error", "error", err)
		return
	}
	if n > 0 {
		slog.Debug("email outbox dispatcher: batch processed", "count", n)
	}
}

// handle is the per-row handler. It demarshals the payload based on
// email_type and dispatches to the matching Send* method. Returning nil
// marks the row 'dispatched'; returning an error schedules a retry (or DLQ
// after MaxOutboxAttempts).
func (d *Dispatcher) handle(ctx context.Context, row OutboxRow) error {
	msg, err := decodeMessage(row.EmailType, row.Payload)
	if err != nil {
		// A payload-decode error is non-retryable — it will fail the same way on
		// every attempt. Return the error so the row rides out the backoff ramp
		// to DLQ where an operator can inspect and decide.
		return fmt.Errorf("decode payload: %w", err)
	}

	switch row.EmailType {
	case TypeInvoice:
		return d.sender.SendInvoice(row.TenantID, msg.To, msg.CustomerName, msg.InvoiceNumber,
			msg.AmountCents, msg.Currency, msg.PDF)
	case TypePaymentReceipt:
		return d.sender.SendPaymentReceipt(row.TenantID, msg.To, msg.CustomerName, msg.InvoiceNumber,
			msg.AmountCents, msg.Currency)
	case TypeDunningWarning:
		return d.sender.SendDunningWarning(row.TenantID, msg.To, msg.CustomerName, msg.InvoiceNumber,
			msg.AttemptNumber, msg.MaxAttempts, msg.NextRetryDate)
	case TypeDunningEscalation:
		return d.sender.SendDunningEscalation(row.TenantID, msg.To, msg.CustomerName, msg.InvoiceNumber,
			msg.Action)
	case TypePaymentFailed:
		return d.sender.SendPaymentFailed(row.TenantID, msg.To, msg.CustomerName, msg.InvoiceNumber,
			msg.Reason)
	case TypePaymentUpdateRequest:
		return d.sender.SendPaymentUpdateRequest(row.TenantID, msg.To, msg.CustomerName, msg.InvoiceNumber,
			msg.AmountCents, msg.Currency, msg.UpdateURL)
	case TypePortalMagicLink:
		return d.sender.SendPortalMagicLink(row.TenantID, msg.To, msg.CustomerName, msg.MagicLinkURL)
	default:
		return fmt.Errorf("unknown email_type %q", row.EmailType)
	}
}

// decodeMessage turns the jsonb payload into a strongly-typed message. The
// schema is intentionally a single union struct (rather than per-type
// structs) because the outbox is a log and we care more about simple
// read-back than strict schema segmentation.
func decodeMessage(emailType string, payload map[string]any) (outboxMessage, error) {
	var msg outboxMessage
	if payload == nil {
		return msg, fmt.Errorf("empty payload")
	}
	// Re-marshal and unmarshal so field parsing uses json tags and numeric
	// normalisation (jsonb comes back as map[string]any with float64 numbers).
	raw, err := json.Marshal(payload)
	if err != nil {
		return msg, err
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		return msg, err
	}
	if msg.To == "" {
		return msg, fmt.Errorf("%s: missing 'to'", emailType)
	}
	return msg, nil
}
