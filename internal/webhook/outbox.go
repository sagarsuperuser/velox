package webhook

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

// OutboxRow is a single queued outbound-event intent. Produced inside the
// business-op transaction, drained by the dispatcher.
type OutboxRow struct {
	ID            string
	TenantID      string
	Livemode      bool // Carries the producer tx's mode; dispatcher propagates this into ctx so delivery hits only same-mode endpoints.
	EventType     string
	Payload       map[string]any
	Status        string
	Attempts      int
	NextAttemptAt time.Time
	LastError     string
	CreatedAt     time.Time
	DispatchedAt  *time.Time
}

// outbox row statuses.
const (
	OutboxPending    = "pending"
	OutboxDispatched = "dispatched"
	OutboxFailed     = "failed"
)

// MaxOutboxAttempts is the DLQ threshold. After this many failed attempts a
// row becomes terminal ('failed') and is no longer retried automatically.
// Backoff ramp (see outboxBackoff) totals ~72h across 15 attempts.
const MaxOutboxAttempts = 15

// outboxBackoff returns the delay before the next attempt given the current
// attempt count (1 = after the first failure). Ramp: 1s, 5s, 30s, 2m, 5m,
// 15m, 30m, 1h, 2h, 4h, 8h, 12h, 12h, 12h, 12h — ~72h total over 15 tries.
func outboxBackoff(attempt int) time.Duration {
	ramp := []time.Duration{
		1 * time.Second,
		5 * time.Second,
		30 * time.Second,
		2 * time.Minute,
		5 * time.Minute,
		15 * time.Minute,
		30 * time.Minute,
		1 * time.Hour,
		2 * time.Hour,
		4 * time.Hour,
		8 * time.Hour,
		12 * time.Hour,
		12 * time.Hour,
		12 * time.Hour,
		12 * time.Hour,
	}
	idx := max(attempt-1, 0)
	if idx >= len(ramp) {
		idx = len(ramp) - 1
	}
	return ramp[idx]
}

// OutboxStore persists webhook-event emission intents.
type OutboxStore struct {
	db *postgres.DB
}

func NewOutboxStore(db *postgres.DB) *OutboxStore {
	return &OutboxStore{db: db}
}

// Enqueue inserts a pending outbox row inside the caller's tx. Use this from
// a business-op store method so the event is persisted atomically with the
// state change — if the tx rolls back, no event is emitted; if it commits,
// the dispatcher will eventually deliver.
func (s *OutboxStore) Enqueue(ctx context.Context, tx *sql.Tx, tenantID, eventType string, payload map[string]any) (string, error) {
	if tenantID == "" {
		return "", fmt.Errorf("outbox: tenant_id required")
	}
	if eventType == "" {
		return "", fmt.Errorf("outbox: event_type required")
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("outbox: marshal payload: %w", err)
	}

	id := postgres.NewID("vlx_whob")
	_, err = tx.ExecContext(ctx, `
		INSERT INTO webhook_outbox (id, tenant_id, event_type, payload, status, attempts, next_attempt_at)
		VALUES ($1, $2, $3, $4, 'pending', 0, now())
	`, id, tenantID, eventType, payloadJSON)
	if err != nil {
		return "", fmt.Errorf("outbox: insert: %w", err)
	}
	return id, nil
}

// EnqueueStandalone opens its own tenant-scoped tx to insert the outbox row.
// Use when the caller has no tx already in scope — still durable because the
// insert commits before return, but not atomic with the preceding business op.
// Prefer Enqueue whenever a tx is available.
func (s *OutboxStore) EnqueueStandalone(ctx context.Context, tenantID, eventType string, payload map[string]any) (string, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return "", fmt.Errorf("outbox: begin tx: %w", err)
	}
	defer postgres.Rollback(tx)

	id, err := s.Enqueue(ctx, tx, tenantID, eventType, payload)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("outbox: commit: %w", err)
	}
	return id, nil
}

// OutboxHandler is called once per claimed row by ProcessBatch. Returning nil
// means the row is dispatched successfully; returning an error schedules a
// retry (or DLQ once MaxOutboxAttempts is reached).
type OutboxHandler func(ctx context.Context, row OutboxRow) error

// ProcessBatch locks up to `limit` due pending rows across all tenants,
// hands them to `handler`, and marks each row based on the handler's result
// — all within a single tx. Row locks held for the tx's duration prevent
// concurrent dispatchers from double-delivering (`FOR UPDATE SKIP LOCKED`).
//
// Returns the number of rows processed (attempted, regardless of outcome).
// Callers should set a sensible query timeout on ctx so a stuck handler
// can't hold locks indefinitely.
func (s *OutboxStore) ProcessBatch(ctx context.Context, limit int, handler OutboxHandler) (int, error) {
	if limit <= 0 {
		limit = 10
	}
	if handler == nil {
		return 0, fmt.Errorf("outbox: handler required")
	}

	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return 0, fmt.Errorf("outbox: begin tx: %w", err)
	}
	defer postgres.Rollback(tx)

	rows, err := tx.QueryContext(ctx, `
		SELECT id, tenant_id, livemode, event_type, payload, status, attempts,
		       next_attempt_at, COALESCE(last_error,''), created_at, dispatched_at
		FROM webhook_outbox
		WHERE status = 'pending' AND next_attempt_at <= now()
		ORDER BY next_attempt_at
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	`, limit)
	if err != nil {
		return 0, fmt.Errorf("outbox: select pending: %w", err)
	}

	var batch []OutboxRow
	for rows.Next() {
		var r OutboxRow
		var payloadJSON []byte
		if err := rows.Scan(&r.ID, &r.TenantID, &r.Livemode, &r.EventType, &payloadJSON,
			&r.Status, &r.Attempts, &r.NextAttemptAt, &r.LastError,
			&r.CreatedAt, &r.DispatchedAt); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("outbox: scan: %w", err)
		}
		if len(payloadJSON) > 0 {
			_ = json.Unmarshal(payloadJSON, &r.Payload)
		}
		batch = append(batch, r)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("outbox: rows err: %w", err)
	}
	_ = rows.Close()

	if len(batch) == 0 {
		return 0, tx.Commit()
	}

	for _, row := range batch {
		hErr := handler(ctx, row)
		if hErr == nil {
			if _, err := tx.ExecContext(ctx, `
				UPDATE webhook_outbox
				SET status = 'dispatched',
				    attempts = attempts + 1,
				    dispatched_at = now(),
				    last_error = NULL
				WHERE id = $1
			`, row.ID); err != nil {
				return 0, fmt.Errorf("outbox: mark dispatched: %w", err)
			}
			continue
		}

		// Failure path — pick retry-with-backoff or DLQ based on attempt count.
		newAttempt := row.Attempts + 1
		errMsg := truncateError(hErr.Error())
		if newAttempt >= MaxOutboxAttempts {
			if _, err := tx.ExecContext(ctx, `
				UPDATE webhook_outbox
				SET status = 'failed',
				    attempts = $2,
				    last_error = $3
				WHERE id = $1
			`, row.ID, newAttempt, errMsg); err != nil {
				return 0, fmt.Errorf("outbox: mark failed: %w", err)
			}
			slog.Error("webhook outbox: row moved to DLQ after max attempts",
				"outbox_id", row.ID,
				"tenant_id", row.TenantID,
				"event_type", row.EventType,
				"attempts", newAttempt,
				"error", errMsg,
			)
			continue
		}

		delay := outboxBackoff(newAttempt)
		nextAt := time.Now().UTC().Add(delay)
		if _, err := tx.ExecContext(ctx, `
			UPDATE webhook_outbox
			SET attempts = $2,
			    next_attempt_at = $3,
			    last_error = $4
			WHERE id = $1
		`, row.ID, newAttempt, nextAt, errMsg); err != nil {
			return 0, fmt.Errorf("outbox: mark retry: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("outbox: commit: %w", err)
	}
	return len(batch), nil
}

// TryDispatcherLock tries to acquire the cluster-wide advisory lock that
// gates the outbox dispatcher tick. Returns (lock, true, nil) on success;
// caller defers lock.Release. Returns (nil, false, nil) if another replica
// holds it. Implements webhook.DispatchLocker.
func (s *OutboxStore) TryDispatcherLock(ctx context.Context) (DispatchLock, bool, error) {
	lock, ok, err := s.db.TryAdvisoryLock(ctx, postgres.LockKeyOutboxDispatcher)
	if err != nil || !ok {
		return nil, ok, err
	}
	return lock, true, nil
}

// PendingCount returns the current number of rows awaiting dispatch. Intended
// for metrics (operator gauge) — not on the hot path.
func (s *OutboxStore) PendingCount(ctx context.Context) (int64, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return 0, err
	}
	defer postgres.Rollback(tx)

	var n int64
	if err := tx.QueryRowContext(ctx,
		`SELECT count(*) FROM webhook_outbox WHERE status = 'pending'`,
	).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// FailedCount returns rows currently in the DLQ — used for alerting. If this
// grows, an endpoint is persistently broken or a producer is emitting
// malformed events.
func (s *OutboxStore) FailedCount(ctx context.Context) (int64, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return 0, err
	}
	defer postgres.Rollback(tx)

	var n int64
	if err := tx.QueryRowContext(ctx,
		`SELECT count(*) FROM webhook_outbox WHERE status = 'failed'`,
	).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func truncateError(s string) string {
	const max = 500
	if len(s) <= max {
		return s
	}
	return s[:max]
}
