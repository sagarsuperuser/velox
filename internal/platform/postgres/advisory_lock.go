package postgres

import (
	"context"
	"database/sql"
	"fmt"
)

// Reserved advisory-lock keys for singleton periodic roles. Keys are stable
// once deployed — changing a value lets an old binary and a new binary each
// acquire concurrently, which defeats the point. Values are arbitrary but
// namespaced high enough (>1e7) to not collide with any key the golang-migrate
// library picks when it hashes schema names.
const (
	LockKeyBillingScheduler int64 = 76540001
	LockKeyDunningScheduler int64 = 76540002
	LockKeyOutboxDispatcher int64 = 76540003
	LockKeyEmailDispatcher  int64 = 76540004
	// LockKeyWebhookRetry gates the webhook delivery retry worker (P5):
	// the claim lease is arithmetic-sized, but leader gating makes the
	// sizing non-critical on multi-replica deploys — the same posture
	// both outbox dispatchers already take.
	LockKeyWebhookRetry int64 = 76540005
	// LockKeyBootstrap serializes tenant bootstrap (ADR-073): taken as
	// pg_advisory_xact_lock inside RunBootstrap's single tx so the
	// first-tenant existence check and the owner-email uniqueness
	// pre-check are authoritative, not check-then-insert TOCTOUs.
	LockKeyBootstrap int64 = 76540006
	// LockKeyMigrateHybrid serializes the ENTIRE hybrid migration loop
	// (ADR-073) — deliberately NOT golang-migrate's derived lock id
	// (same-id on a second session would deadlock the library's own
	// Lock()). Held for the loop's whole duration so per-iteration
	// version reads are authoritative across racing replicas.
	LockKeyMigrateHybrid int64 = 76540007
	// LockKeyTopologyCheck is used ONLY by VerifyAdvisoryLockTopology's
	// boot probe — acquired and released within the check, never held.
	LockKeyTopologyCheck int64 = 76540008
)

// AdvisoryLock is a held Postgres session-scoped advisory lock. Release MUST
// be called — preferably via defer — to free the lock and return the
// underlying connection to the pool. Sleeping the connection out of the pool
// is fine: we hold one conn per lock for as long as the tick runs (seconds).
type AdvisoryLock struct {
	conn *sql.Conn
	key  int64
}

// TryAdvisoryLock attempts to acquire a session-scoped advisory lock keyed on
// `key`. Returns (lock, true, nil) on success — caller defers lock.Release.
// Returns (nil, false, nil) if another session already holds the lock —
// caller should skip its tick. Returns (nil, false, err) on infrastructure
// failure (conn checkout or query error).
//
// Why session-scoped (not transactional): scheduler ticks span many separate
// queries/transactions, and pg_try_advisory_xact_lock would release the moment
// the first tx commits. Session-scoped holds until we explicitly unlock.
//
// Leader failure safety: if this process crashes mid-tick, the TCP connection
// dies, Postgres closes the session, and the lock is auto-released — another
// replica picks up on its next tick. No zombie-lock recovery needed.
func (db *DB) TryAdvisoryLock(ctx context.Context, key int64) (*AdvisoryLock, bool, error) {
	conn, err := db.Pool.Conn(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("advisory lock: checkout conn: %w", err)
	}

	var ok bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", key).Scan(&ok); err != nil {
		_ = conn.Close()
		return nil, false, fmt.Errorf("advisory lock: try acquire key=%d: %w", key, err)
	}
	if !ok {
		_ = conn.Close()
		return nil, false, nil
	}

	return &AdvisoryLock{conn: conn, key: key}, true, nil
}

// Release frees the lock and returns the connection to the pool. Uses a
// fresh background context so shutdown-triggered cancellation on the tick
// context doesn't leave the lock held until the connection ages out.
//
// Safe to call multiple times; no-op if the lock was never acquired.
func (l *AdvisoryLock) Release() {
	if l == nil || l.conn == nil {
		return
	}
	_, _ = l.conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", l.key)
	_ = l.conn.Close()
	l.conn = nil
}

// VerifyAdvisoryLockTopology is the boot-time self-check that the
// connection topology actually supports session-scoped advisory locks.
//
// Every singleton worker (billing scheduler, dunning, both outbox
// dispatchers, webhook retry) gates its tick on pg_try_advisory_lock
// held across a SESSION. Behind a transaction-mode pooler (PgBouncer
// transaction/statement mode, RDS Proxy with pinning disabled) each
// statement can run on a DIFFERENT server session: the unlock then
// executes on a session that doesn't hold the lock, the original server
// session keeps it forever, and every future tick on every replica
// skips as "another leader holds the lock" — billing silently halts.
// That failure mode produces no error anywhere, so it MUST be caught at
// boot, not diagnosed from a week of missing invoices.
//
// Two probes, both deterministic-clean on direct Postgres and on
// session-mode PgBouncer (each client conn = one server session):
//
//  1. Same pinned conn must keep one backend PID across statements —
//     a PID flip mid-conn is transaction pooling, full stop.
//  2. A lock held on conn A must be invisible-to-acquire on conn B, and
//     A's unlock must return true. B acquiring A's key means both
//     client conns share a server session; unlock=false means the
//     unlock ran on a session that never took the lock. Either way the
//     leader gate is broken.
//
// A nil return does not prove the pooler is safe under load (a lucky
// route can hide pooling), but any error is definite misconfiguration —
// callers should refuse to start.
func (db *DB) VerifyAdvisoryLockTopology(ctx context.Context) error {
	connA, err := db.Pool.Conn(ctx)
	if err != nil {
		return fmt.Errorf("advisory-lock topology check: checkout conn A: %w", err)
	}
	defer func() { _ = connA.Close() }()

	var pid1, pid2 int
	if err := connA.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid1); err != nil {
		return fmt.Errorf("advisory-lock topology check: read backend pid: %w", err)
	}

	var got bool
	if err := connA.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", LockKeyTopologyCheck).Scan(&got); err != nil {
		return fmt.Errorf("advisory-lock topology check: acquire probe lock: %w", err)
	}
	if !got {
		// Another replica is running its own boot check right now —
		// harmless; its passing verdict covers this topology too.
		return nil
	}

	if err := connA.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid2); err != nil {
		return fmt.Errorf("advisory-lock topology check: re-read backend pid: %w", err)
	}
	if pid1 != pid2 {
		return fmt.Errorf("advisory-lock topology check: backend PID changed mid-connection (%d → %d) — a transaction-mode pooler is between Velox and Postgres; session advisory locks would strand and silently halt the billing scheduler. Use direct connections or PgBouncer session mode (docs/ops/postgres-requirements.md)", pid1, pid2)
	}

	connB, err := db.Pool.Conn(ctx)
	if err != nil {
		return fmt.Errorf("advisory-lock topology check: checkout conn B: %w", err)
	}
	defer func() { _ = connB.Close() }()

	var reacquired bool
	if err := connB.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", LockKeyTopologyCheck).Scan(&reacquired); err != nil {
		return fmt.Errorf("advisory-lock topology check: contend probe lock: %w", err)
	}
	if reacquired {
		// Same-session reentrancy: both client conns hit one server
		// session. Undo the extra grab before erroring.
		_, _ = connB.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", LockKeyTopologyCheck)
		_, _ = connA.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", LockKeyTopologyCheck)
		return fmt.Errorf("advisory-lock topology check: two pooled connections share one Postgres session — a transaction-mode pooler is between Velox and Postgres; singleton-worker leader election would misfire. Use direct connections or PgBouncer session mode (docs/ops/postgres-requirements.md)")
	}

	var unlocked bool
	if err := connA.QueryRowContext(ctx, "SELECT pg_advisory_unlock($1)", LockKeyTopologyCheck).Scan(&unlocked); err != nil {
		return fmt.Errorf("advisory-lock topology check: release probe lock: %w", err)
	}
	if !unlocked {
		return fmt.Errorf("advisory-lock topology check: unlock ran on a session that never took the lock — a transaction-mode pooler is between Velox and Postgres; the probe lock is now stranded exactly as scheduler locks would be. Use direct connections or PgBouncer session mode (docs/ops/postgres-requirements.md)")
	}
	return nil
}
