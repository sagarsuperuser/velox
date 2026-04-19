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
