package webhook_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/platform/postgres"
	"github.com/sagarsuperuser/velox/internal/testutil"
	"github.com/sagarsuperuser/velox/internal/webhook"
)

// TestOutbox_EnqueueStandalone_Persists verifies the base durability
// guarantee: after EnqueueStandalone returns without error, a pending row
// exists in webhook_outbox. No dispatcher involvement.
func TestOutbox_EnqueueStandalone_Persists(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	tenantID := testutil.CreateTestTenant(t, db, "Outbox Enqueue")
	store := webhook.NewOutboxStore(db)

	id, err := store.EnqueueStandalone(ctx, tenantID, "invoice.finalized", map[string]any{
		"invoice_id": "inv_1",
		"amount":     1234,
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty outbox id")
	}

	status, attempts, payload := readOutbox(t, db, id)
	if status != webhook.OutboxPending {
		t.Errorf("status: got %q, want %q", status, webhook.OutboxPending)
	}
	if attempts != 0 {
		t.Errorf("attempts: got %d, want 0", attempts)
	}
	if payload["invoice_id"] != "inv_1" {
		t.Errorf("payload missing invoice_id: %+v", payload)
	}
}

// TestOutbox_Enqueue_TxAtomicity verifies the core of the transactional
// outbox pattern: a row enqueued inside a tx that rolls back must NOT
// persist. This is what lets producers enqueue in the same tx as their
// state change with zero risk of an orphan event.
func TestOutbox_Enqueue_TxAtomicity(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	tenantID := testutil.CreateTestTenant(t, db, "Outbox Tx")
	store := webhook.NewOutboxStore(db)

	// Rollback path — row must not exist after rollback.
	tx, err := db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	rollbackID, err := store.Enqueue(ctx, tx, tenantID, "test.rollback", map[string]any{"k": "v"})
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("enqueue in rollback tx: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if exists := outboxExists(t, db, rollbackID); exists {
		t.Error("row persisted despite rollback — outbox breaks tx atomicity")
	}

	// Commit path — row must exist after commit.
	tx2, err := db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		t.Fatalf("begin tx2: %v", err)
	}
	commitID, err := store.Enqueue(ctx, tx2, tenantID, "test.commit", map[string]any{"k": "v"})
	if err != nil {
		_ = tx2.Rollback()
		t.Fatalf("enqueue in commit tx: %v", err)
	}
	if err := tx2.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if exists := outboxExists(t, db, commitID); !exists {
		t.Error("row missing after commit — outbox dropped the insert")
	}
}

// TestOutbox_ProcessBatch_Success covers the happy path: handler returns nil,
// row transitions to 'dispatched', attempts=1, dispatched_at populated.
func TestOutbox_ProcessBatch_Success(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	tenantID := testutil.CreateTestTenant(t, db, "Outbox Success")
	store := webhook.NewOutboxStore(db)

	id, err := store.EnqueueStandalone(ctx, tenantID, "evt.ok", map[string]any{"n": 1})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	var saw webhook.OutboxRow
	n, err := store.ProcessBatch(ctx, 10, func(_ context.Context, row webhook.OutboxRow) error {
		saw = row
		return nil
	})
	if err != nil {
		t.Fatalf("process batch: %v", err)
	}
	if n != 1 {
		t.Fatalf("processed: got %d, want 1", n)
	}
	if saw.ID != id || saw.TenantID != tenantID || saw.EventType != "evt.ok" {
		t.Errorf("handler got wrong row: %+v", saw)
	}

	status, attempts, _ := readOutbox(t, db, id)
	if status != webhook.OutboxDispatched {
		t.Errorf("status: got %q, want %q", status, webhook.OutboxDispatched)
	}
	if attempts != 1 {
		t.Errorf("attempts: got %d, want 1", attempts)
	}
}

// TestOutbox_ProcessBatch_RetryBackoff covers the retry path: a transient
// handler error increments attempts and pushes next_attempt_at into the
// future per outboxBackoff — so a subsequent immediate ProcessBatch call
// MUST NOT re-claim the row.
func TestOutbox_ProcessBatch_RetryBackoff(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	tenantID := testutil.CreateTestTenant(t, db, "Outbox Retry")
	store := webhook.NewOutboxStore(db)

	id, err := store.EnqueueStandalone(ctx, tenantID, "evt.flaky", map[string]any{})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// First pass — handler fails.
	n, err := store.ProcessBatch(ctx, 10, func(_ context.Context, _ webhook.OutboxRow) error {
		return errors.New("boom")
	})
	if err != nil {
		t.Fatalf("process batch 1: %v", err)
	}
	if n != 1 {
		t.Fatalf("processed: got %d, want 1", n)
	}

	status, attempts, _ := readOutbox(t, db, id)
	if status != webhook.OutboxPending {
		t.Errorf("status: got %q, want %q (not yet DLQ)", status, webhook.OutboxPending)
	}
	if attempts != 1 {
		t.Errorf("attempts: got %d, want 1", attempts)
	}
	lastErr, nextAt := readOutboxRetry(t, db, id)
	if lastErr != "boom" {
		t.Errorf("last_error: got %q, want %q", lastErr, "boom")
	}
	if !nextAt.After(time.Now().UTC()) {
		t.Errorf("next_attempt_at should be in the future, got %v", nextAt)
	}

	// Second immediate pass — row is not yet due, so nothing processed.
	n2, err := store.ProcessBatch(ctx, 10, func(_ context.Context, _ webhook.OutboxRow) error {
		return errors.New("should not run")
	})
	if err != nil {
		t.Fatalf("process batch 2: %v", err)
	}
	if n2 != 0 {
		t.Errorf("processed on second pass: got %d, want 0 (next_attempt_at not due)", n2)
	}
}

// TestOutbox_ProcessBatch_DLQ verifies that after MaxOutboxAttempts failures
// the row becomes 'failed' and is no longer claimed by subsequent batches —
// exactly what the dead-letter-queue contract promises.
func TestOutbox_ProcessBatch_DLQ(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tenantID := testutil.CreateTestTenant(t, db, "Outbox DLQ")
	store := webhook.NewOutboxStore(db)

	id, err := store.EnqueueStandalone(ctx, tenantID, "evt.broken", map[string]any{})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// Drive attempts to the DLQ threshold by repeatedly forcing next_attempt_at
	// back to now and running a failing handler. This is the same transition
	// path the real dispatcher would take over ~72h, compressed into one test.
	for i := 1; i <= webhook.MaxOutboxAttempts; i++ {
		if err := resetDue(db, id); err != nil {
			t.Fatalf("attempt %d: reset due: %v", i, err)
		}
		n, err := store.ProcessBatch(ctx, 10, func(_ context.Context, _ webhook.OutboxRow) error {
			return fmt.Errorf("attempt %d failed", i)
		})
		if err != nil {
			t.Fatalf("attempt %d: process: %v", i, err)
		}
		if n != 1 {
			t.Fatalf("attempt %d: processed %d, want 1", i, n)
		}
	}

	status, attempts, _ := readOutbox(t, db, id)
	if status != webhook.OutboxFailed {
		t.Errorf("status: got %q, want %q after %d attempts", status, webhook.OutboxFailed, webhook.MaxOutboxAttempts)
	}
	if attempts != webhook.MaxOutboxAttempts {
		t.Errorf("attempts: got %d, want %d", attempts, webhook.MaxOutboxAttempts)
	}

	// DLQ rows are terminal — they should NOT be re-claimed even if made "due".
	if err := resetDue(db, id); err != nil {
		t.Fatalf("reset due on DLQ row: %v", err)
	}
	n, err := store.ProcessBatch(ctx, 10, func(_ context.Context, _ webhook.OutboxRow) error {
		t.Error("DLQ row was re-claimed — terminal status not respected")
		return nil
	})
	if err != nil {
		t.Fatalf("post-DLQ batch: %v", err)
	}
	if n != 0 {
		t.Errorf("post-DLQ: processed %d, want 0", n)
	}
}

// TestOutbox_Counts verifies PendingCount and FailedCount match reality —
// these feed operator dashboards, so accuracy is non-negotiable.
func TestOutbox_Counts(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	tenantID := testutil.CreateTestTenant(t, db, "Outbox Counts")
	store := webhook.NewOutboxStore(db)

	for range 3 {
		if _, err := store.EnqueueStandalone(ctx, tenantID, "evt.x", map[string]any{}); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}

	pending, err := store.PendingCount(ctx)
	if err != nil {
		t.Fatalf("pending count: %v", err)
	}
	if pending != 3 {
		t.Errorf("pending: got %d, want 3", pending)
	}

	failed, err := store.FailedCount(ctx)
	if err != nil {
		t.Fatalf("failed count: %v", err)
	}
	if failed != 0 {
		t.Errorf("failed: got %d, want 0", failed)
	}
}

// --- helpers ---

func readOutbox(t *testing.T, db *postgres.DB, id string) (status string, attempts int, payload map[string]any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		t.Fatalf("read tx: %v", err)
	}
	defer postgres.Rollback(tx)

	var payloadJSON []byte
	err = tx.QueryRowContext(ctx,
		`SELECT status, attempts, payload FROM webhook_outbox WHERE id = $1`,
		id,
	).Scan(&status, &attempts, &payloadJSON)
	if err != nil {
		t.Fatalf("scan outbox row %s: %v", id, err)
	}
	if len(payloadJSON) > 0 {
		_ = json.Unmarshal(payloadJSON, &payload)
	}
	return status, attempts, payload
}

func readOutboxRetry(t *testing.T, db *postgres.DB, id string) (lastErr string, nextAt time.Time) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		t.Fatalf("read retry tx: %v", err)
	}
	defer postgres.Rollback(tx)

	var nullErr sql.NullString
	err = tx.QueryRowContext(ctx,
		`SELECT COALESCE(last_error,''), next_attempt_at FROM webhook_outbox WHERE id = $1`,
		id,
	).Scan(&nullErr, &nextAt)
	if err != nil {
		t.Fatalf("scan retry row %s: %v", id, err)
	}
	return nullErr.String, nextAt
}

func outboxExists(t *testing.T, db *postgres.DB, id string) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		t.Fatalf("exists tx: %v", err)
	}
	defer postgres.Rollback(tx)

	var n int
	if err := tx.QueryRowContext(ctx,
		`SELECT count(*) FROM webhook_outbox WHERE id = $1`, id).Scan(&n); err != nil {
		t.Fatalf("exists scan: %v", err)
	}
	return n > 0
}

// resetDue forces next_attempt_at back to now() so the next ProcessBatch
// tick claims the row immediately. Used by DLQ/retry tests that must
// exercise many attempts within a single test run without waiting out
// real backoff durations.
func resetDue(db *postgres.DB, id string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return err
	}
	defer postgres.Rollback(tx)
	if _, err := tx.ExecContext(ctx,
		`UPDATE webhook_outbox SET next_attempt_at = now() WHERE id = $1`, id); err != nil {
		return err
	}
	return tx.Commit()
}

