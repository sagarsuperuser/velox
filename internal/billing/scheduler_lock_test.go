package billing

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

// fakeLocker lets a test simulate two replicas racing for the same key.
// Keeps a per-key boolean; TryAdvisoryLock flips it true if free (returns a
// Lock whose Release flips it back), or returns false if already held.
type fakeLocker struct {
	held map[int64]*atomic.Bool
	// acquireErr is returned from TryAdvisoryLock — used to exercise the
	// error branch (infrastructure failure) without hitting Postgres.
	acquireErr error
	// acquireCalls counts the total acquire attempts, for asserting that a
	// skipped tick still polled the lock.
	acquireCalls atomic.Int32
}

func newFakeLocker() *fakeLocker {
	return &fakeLocker{held: map[int64]*atomic.Bool{}}
}

func (f *fakeLocker) TryAdvisoryLock(_ context.Context, key int64) (Lock, bool, error) {
	f.acquireCalls.Add(1)
	if f.acquireErr != nil {
		return nil, false, f.acquireErr
	}
	slot, ok := f.held[key]
	if !ok {
		slot = &atomic.Bool{}
		f.held[key] = slot
	}
	if !slot.CompareAndSwap(false, true) {
		return nil, false, nil
	}
	return &fakeLock{slot: slot}, true, nil
}

type fakeLock struct {
	slot     *atomic.Bool
	released atomic.Bool
}

func (l *fakeLock) Release() {
	if l.released.CompareAndSwap(false, true) {
		l.slot.Store(false)
	}
}

// ---- Minimal dependency stubs -------------------------------------------
//
// The scheduler runBillingHalf / runDunningHalf paths touch engine.RunCycle,
// engine.RetryPendingCharges, dunning.ProcessDueRuns, and tenants.ListTenantIDs.
// All are counted so the test asserts whether the leader-gated body actually
// ran for a given tick.

type countingDunning struct {
	calls atomic.Int32

	// modesMu guards modes; the scheduler fans out per livemode so every
	// tick should record both true and false at least once per tenant.
	modesMu sync.Mutex
	modes   []bool
}

func (c *countingDunning) ProcessDueRuns(ctx context.Context, _ string, _ int) (int, []error) {
	c.calls.Add(1)
	c.modesMu.Lock()
	c.modes = append(c.modes, postgres.Livemode(ctx))
	c.modesMu.Unlock()
	return 0, nil
}

func (c *countingDunning) observedModes() []bool {
	c.modesMu.Lock()
	defer c.modesMu.Unlock()
	out := make([]bool, len(c.modes))
	copy(out, c.modes)
	return out
}

type fixedTenants struct {
	ids []string
}

func (t *fixedTenants) ListTenantIDs(_ context.Context) ([]string, error) {
	return t.ids, nil
}

// TestScheduler_LeaderGate_BillingHalf verifies that when two Scheduler
// instances share a Locker and race to tick together, the billing half runs
// exactly once — the follower sees the key already held and skips.
func TestScheduler_LeaderGate_BillingHalf(t *testing.T) {
	t.Parallel()

	locker := newFakeLocker()

	// Engine{} with no deps: RunCycle needs SubscriptionLister; Leave nil
	// and call RunCycle directly is unsafe. Instead, skip RunCycle by
	// giving Engine a no-op SubscriptionLister via the exported Engine
	// struct default — easier: test only the lock-gate behaviour through
	// a lightweight spy.
	//
	// We don't need to exercise the whole body. Instead we observe
	// acquireCalls + released state on fakeLock.

	const billingKey int64 = 1001

	follower := &Scheduler{
		engine:         &Engine{},
		batch:          1,
		locker:         locker,
		billingLockKey: billingKey,
		dunningLockKey: 1002,
	}

	// Simulate a leader tick in flight by pre-acquiring the billing lock
	// directly; the follower's runBillingHalf must then skip.
	leadLock, ok, err := locker.TryAdvisoryLock(context.Background(), billingKey)
	if err != nil || !ok {
		t.Fatalf("leader failed to acquire lock: err=%v ok=%v", err, ok)
	}

	startCalls := locker.acquireCalls.Load()
	// The lock check fires before any engine call, so runBillingHalf is safe
	// to invoke on a Scheduler with a zero-value Engine — it never reaches
	// the body.
	follower.runBillingHalf(context.Background())
	if got := locker.acquireCalls.Load() - startCalls; got != 1 {
		t.Fatalf("follower should have attempted lock once, got %d", got)
	}

	// Leader releases; the key must be free again.
	leadLock.Release()
	relockLock, ok, err := locker.TryAdvisoryLock(context.Background(), billingKey)
	if err != nil || !ok {
		t.Fatalf("lock not released after leader Release: err=%v ok=%v", err, ok)
	}
	relockLock.Release()
}

// TestScheduler_LeaderGate_DunningHalfRuns verifies the dunning path actually
// executes when the lock is free. Uses real stubs so we can assert the body
// fired.
func TestScheduler_LeaderGate_DunningHalfRuns(t *testing.T) {
	t.Parallel()

	locker := newFakeLocker()
	dunning := &countingDunning{}

	s := &Scheduler{
		engine:         &Engine{},
		dunning:        dunning,
		tenants:        &fixedTenants{ids: []string{"t_1", "t_2"}},
		batch:          1,
		locker:         locker,
		billingLockKey: 2001,
		dunningLockKey: 2002,
	}

	s.runDunningHalf(context.Background())

	// Fan-out: 2 tenants × 2 livemodes = 4 calls per tick.
	if got := dunning.calls.Load(); got != 4 {
		t.Fatalf("dunning body should have fired four times (2 tenants × 2 modes); got %d", got)
	}

	// After release, the key must be free again — another runDunningHalf
	// call should acquire the lock and run again.
	s.runDunningHalf(context.Background())
	if got := dunning.calls.Load(); got != 8 {
		t.Fatalf("second dunning tick should have fired after lock released; got %d", got)
	}
}

// TestScheduler_DunningFansOutPerLivemode verifies #13's core guarantee: every
// tick invokes the dunning body once per livemode per tenant, and each call
// carries the correct livemode in ctx (not the default-to-live fallback).
func TestScheduler_DunningFansOutPerLivemode(t *testing.T) {
	t.Parallel()

	dunning := &countingDunning{}
	s := &Scheduler{
		engine:  &Engine{},
		dunning: dunning,
		tenants: &fixedTenants{ids: []string{"t_1"}},
		batch:   1,
	}

	s.runDunningHalf(context.Background())

	modes := dunning.observedModes()
	if len(modes) != 2 {
		t.Fatalf("expected 2 calls (1 tenant × 2 modes); got %d", len(modes))
	}
	var sawLive, sawTest bool
	for _, m := range modes {
		if m {
			sawLive = true
		} else {
			sawTest = true
		}
	}
	if !sawLive || !sawTest {
		t.Fatalf("fan-out must cover both live and test modes; saw live=%v test=%v", sawLive, sawTest)
	}
}

// TestScheduler_RunDunningForMode_PanicsWithoutLivemode is the regression
// guard for #14: runDunningForMode is a mode-aware entry point and must
// panic if its caller forgot to wrap ctx with WithLivemode. Without this
// assertion the scheduler would silently route test-mode work into the
// live partition via the default-to-live fallback.
func TestScheduler_RunDunningForMode_PanicsWithoutLivemode(t *testing.T) {
	t.Parallel()

	s := &Scheduler{
		engine:  &Engine{},
		dunning: &countingDunning{},
		tenants: &fixedTenants{ids: []string{"t_1"}},
		batch:   1,
	}

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("runDunningForMode should panic on ctx without WithLivemode")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "without explicit livemode") {
			t.Fatalf("panic should mention missing livemode; got %q", msg)
		}
	}()
	s.runDunningForMode(context.Background(), true, []string{"t_1"})
}

// TestScheduler_LeaderGate_DunningHalfSkipsWhenHeld verifies that when the
// dunning lock is already held elsewhere, the dunning body does NOT run.
func TestScheduler_LeaderGate_DunningHalfSkipsWhenHeld(t *testing.T) {
	t.Parallel()

	locker := newFakeLocker()
	dunning := &countingDunning{}

	s := &Scheduler{
		engine:         &Engine{},
		dunning:        dunning,
		tenants:        &fixedTenants{ids: []string{"t_1"}},
		batch:          1,
		locker:         locker,
		billingLockKey: 3001,
		dunningLockKey: 3002,
	}

	// Pre-acquire the dunning lock — simulate another replica holding it.
	held, ok, err := locker.TryAdvisoryLock(context.Background(), s.dunningLockKey)
	if err != nil || !ok {
		t.Fatalf("pre-acquire failed: err=%v ok=%v", err, ok)
	}
	defer held.Release()

	s.runDunningHalf(context.Background())

	if got := dunning.calls.Load(); got != 0 {
		t.Fatalf("dunning body must not run when lock held; got %d calls", got)
	}
}

// TestScheduler_LeaderGate_LockError propagates-and-skips cleanly — if the
// lock infra itself fails, we must not attempt the gated work.
func TestScheduler_LeaderGate_LockError(t *testing.T) {
	t.Parallel()

	locker := newFakeLocker()
	locker.acquireErr = errors.New("db down")

	dunning := &countingDunning{}
	s := &Scheduler{
		engine:         &Engine{},
		dunning:        dunning,
		tenants:        &fixedTenants{ids: []string{"t_1"}},
		batch:          1,
		locker:         locker,
		billingLockKey: 4001,
		dunningLockKey: 4002,
	}

	s.runDunningHalf(context.Background())
	if got := dunning.calls.Load(); got != 0 {
		t.Fatalf("dunning body must not run on lock error; got %d", got)
	}
}

// TestScheduler_LockerNil_RunsUngated confirms a Scheduler with no Locker set
// still runs its body — this is the single-replica default and the test-mode
// escape hatch.
func TestScheduler_LockerNil_RunsUngated(t *testing.T) {
	t.Parallel()

	dunning := &countingDunning{}
	s := &Scheduler{
		engine:  &Engine{},
		dunning: dunning,
		tenants: &fixedTenants{ids: []string{"t_1"}},
		batch:   1,
		// locker: nil (default)
	}

	s.runDunningHalf(context.Background())
	// Fan-out: 1 tenant × 2 livemodes = 2 calls per tick even without a locker.
	if got := dunning.calls.Load(); got != 2 {
		t.Fatalf("nil locker should let body run once per tenant per mode; got %d", got)
	}
}
