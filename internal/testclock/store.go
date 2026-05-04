// Package testclock owns the TestClock resource: a tenant-scoped frozen-time
// simulator used to walk test-mode subscriptions through full billing
// lifecycles (trials, cycles, dunning retries) in compressed wall-clock time.
//
// Clocks exist only in test mode — the underlying table and its CHECK
// constraint enforce livemode=false. Live-mode callers cannot create, advance,
// or attach clocks; attempting to reach the endpoints from a live key produces
// a 400 from the service guard before the DB even sees the write.
package testclock

import (
	"context"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// Store is the persistence contract for TestClock rows. Kept narrow: create,
// read, list, atomic status transitions, atomic advance, delete. All RLS-
// scoped via postgres.TxTenant in the PostgresStore.
type Store interface {
	Create(ctx context.Context, tenantID string, clk domain.TestClock) (domain.TestClock, error)
	Get(ctx context.Context, tenantID, id string) (domain.TestClock, error)
	List(ctx context.Context, tenantID string) ([]domain.TestClock, error)
	Delete(ctx context.Context, tenantID, id string) error

	// MarkAdvancing flips status ready → advancing and simultaneously sets
	// the new frozen_time, atomically. The new frozen_time becomes visible
	// immediately — this is what lets the billing-catchup loop find subs
	// on the clock as "due". Returns errs.InvalidState when the clock is
	// not currently ready (prevents overlapping advances).
	//
	// The new frozen_time must be >= the current frozen_time; the service
	// enforces that up-front so we don't ship a regressed-time clock.
	MarkAdvancing(ctx context.Context, tenantID, id string, newFrozenTime time.Time) (domain.TestClock, error)

	// CompleteAdvance flips status advancing → ready. Pair with MarkAdvancing
	// at the end of a successful catchup run. Clears any
	// last_failure_reason from a prior failed-then-retried advance.
	CompleteAdvance(ctx context.Context, tenantID, id string) (domain.TestClock, error)
	// MarkFailed flips advancing → internal_failure when a catchup run
	// errors. The reason is persisted on the clock row so the dashboard
	// can show "Catchup failed: <reason>" without forcing the operator
	// to dig through server logs. Truncated by the caller to ~500
	// chars; the full payload stays in structured slog. ADR-018.
	MarkFailed(ctx context.Context, tenantID, id, reason string) (domain.TestClock, error)
	// RetryFromFailed transitions internal_failure → advancing on a
	// clock the operator has chosen to retry. Frozen_time is unchanged
	// — the catchup loop only processes subs whose next_billing_at <=
	// frozen_time, so resuming from where the previous attempt
	// stopped is idempotent. The caller then enqueues a fresh
	// CatchupJob; the worker drains it like any other advance.
	// ADR-018.
	RetryFromFailed(ctx context.Context, tenantID, id string) (domain.TestClock, error)

	// ListSubscriptionsOnClock returns every sub attached to the clock. Used
	// by the service during advance to drive the billing catchup. RLS scopes
	// the result to the tenant already; clock-ID filter narrows further.
	ListSubscriptionsOnClock(ctx context.Context, tenantID, clockID string) ([]domain.Subscription, error)

	// ListAllAdvancing returns every clock currently in status='advancing'
	// across ALL tenants. Used at boot to recover catchup jobs that were
	// in-flight when the previous process exited (server restart, deploy,
	// crash). RLS-bypassed because it needs to surface clocks for tenants
	// the caller isn't scoped to. The recovery path then re-enqueues each
	// onto the catchup queue and the worker resumes them.
	ListAllAdvancing(ctx context.Context) ([]domain.TestClock, error)

	// SweepDueDeletes soft-deletes test clocks whose deletes_after has
	// elapsed, cascade-cancelling their pinned subs. Cross-tenant
	// (RLS-bypassed). Returns the number of clocks soft-deleted in this
	// sweep — caller logs the count for liveness telemetry.
	SweepDueDeletes(ctx context.Context, batch int) (int, error)
}
