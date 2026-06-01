package user

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/clock"
)

// ---- memFailureCounter ----------------------------------------------------

func TestMemFailureCounter_IncrementAndKey(t *testing.T) {
	c := newMemFailureCounter(clock.NewFake(time.Unix(1000, 0)))
	ctx := context.Background()
	for want := 1; want <= 3; want++ {
		got, _ := c.Increment(ctx, "alice@x.com")
		if got != want {
			t.Fatalf("increment %d: got %d", want, got)
		}
	}
	// Case- and whitespace-insensitive: shares the same counter.
	if got, _ := c.Increment(ctx, "  ALICE@X.com "); got != 4 {
		t.Errorf("case/space-insensitive key: got %d, want 4", got)
	}
}

func TestMemFailureCounter_WindowExpiry(t *testing.T) {
	clk := clock.NewFake(time.Unix(1000, 0))
	c := newMemFailureCounter(clk)
	ctx := context.Background()
	_, _ = c.Increment(ctx, "a@x")
	_, _ = c.Increment(ctx, "a@x")
	if got, _ := c.Increment(ctx, "a@x"); got != 3 {
		t.Fatalf("pre-expiry: got %d, want 3", got)
	}
	clk.Advance(LockoutDuration + time.Second) // window lapses
	if got, _ := c.Increment(ctx, "a@x"); got != 1 {
		t.Errorf("post-expiry: got %d, want 1 (window reset)", got)
	}
}

func TestMemFailureCounter_WindowNotRefreshedByIncrements(t *testing.T) {
	clk := clock.NewFake(time.Unix(1000, 0))
	c := newMemFailureCounter(clk)
	ctx := context.Background()
	// Fast attacker: keep hitting just under the window; the TTL must NOT be
	// pushed out by later increments, so it still expires LockoutDuration
	// after the FIRST failure.
	_, _ = c.Increment(ctx, "a@x") // sets expiry = t0 + LockoutDuration
	clk.Advance(LockoutDuration - time.Minute)
	if got, _ := c.Increment(ctx, "a@x"); got != 2 {
		t.Fatalf("within window: got %d, want 2", got)
	}
	clk.Advance(2 * time.Minute) // now past the ORIGINAL window
	if got, _ := c.Increment(ctx, "a@x"); got != 1 {
		t.Errorf("window must expire from first failure, not be refreshed: got %d, want 1", got)
	}
}

func TestMemFailureCounter_Reset(t *testing.T) {
	c := newMemFailureCounter(clock.NewFake(time.Unix(1000, 0)))
	ctx := context.Background()
	_, _ = c.Increment(ctx, "a@x")
	_, _ = c.Increment(ctx, "a@x")
	_ = c.Reset(ctx, "a@x")
	if got, _ := c.Increment(ctx, "a@x"); got != 1 {
		t.Errorf("post-reset: got %d, want 1", got)
	}
}

func TestMemFailureCounter_Concurrent(t *testing.T) {
	c := newMemFailureCounter(clock.NewFake(time.Unix(1000, 0)))
	ctx := context.Background()
	const n = 200
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = c.Increment(ctx, "race@x") }()
	}
	wg.Wait()
	// Final count == n (run with -race to catch map/torn-count bugs).
	c.mu.Lock()
	got := c.counts[memKey("race@x")].n
	c.mu.Unlock()
	if got != n {
		t.Errorf("concurrent increments: got %d, want %d", got, n)
	}
}

func TestMemFailureCounter_JanitorBounds(t *testing.T) {
	clk := clock.NewFake(time.Unix(1000, 0))
	c := newMemFailureCounter(clk)
	ctx := context.Background()
	for i := 0; i < 500; i++ {
		_, _ = c.Increment(ctx, string(rune('a'+i%26))+time.Duration(i).String())
	}
	clk.Advance(LockoutDuration + time.Second) // all windows lapse
	_, _ = c.Increment(ctx, "trigger@x")       // crossing the interval triggers a prune
	c.mu.Lock()
	size := len(c.counts)
	c.mu.Unlock()
	if size != 1 {
		t.Errorf("janitor: map size %d, want 1 (expired pruned)", size)
	}
}

// TestMemFailureCounter_SweepThrottled guards the velox-ops #21 adversarial-
// review fix: the O(n) prune must NOT run on every Increment (that let a
// distinct-email flood during a Redis outage add O(n) latency to legitimate
// logins). It runs at most once per memSweepInterval.
func TestMemFailureCounter_SweepThrottled(t *testing.T) {
	clk := clock.NewFake(time.Unix(1000, 0))
	c := newMemFailureCounter(clk)
	ctx := context.Background()
	_, _ = c.Increment(ctx, "a@x") // first call runs the sweep once (lastSwept was zero)
	for i := 0; i < 50; i++ {
		_, _ = c.Increment(ctx, "a@x") // all within the interval — no further sweeps
	}
	c.mu.Lock()
	runs := c.sweepRuns
	c.mu.Unlock()
	if runs != 1 {
		t.Fatalf("sweepRuns within one interval: got %d, want 1 (hot path must stay O(1))", runs)
	}
	clk.Advance(memSweepInterval + time.Second)
	_, _ = c.Increment(ctx, "a@x") // crossing the interval allows exactly one more
	c.mu.Lock()
	runs = c.sweepRuns
	c.mu.Unlock()
	if runs != 2 {
		t.Errorf("sweepRuns after crossing interval: got %d, want 2", runs)
	}
}

// ---- FallbackFailureCounter -----------------------------------------------

// fakeRemote is an injectable remote counter for breaker/fallback tests.
type fakeRemote struct {
	mu     sync.Mutex
	val    int
	err    error // when set, Increment returns this error
	calls  int
	resets int
}

func (r *fakeRemote) Increment(_ context.Context, _ string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.err != nil {
		return 0, r.err
	}
	r.val++
	return r.val, nil
}
func (r *fakeRemote) Reset(_ context.Context, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resets++
	r.val = 0
	return nil
}

func newFallbackWith(remote FailureCounter, clk clock.Clock) *FallbackFailureCounter {
	return &FallbackFailureCounter{remote: remote, local: newMemFailureCounter(clk), clk: clk}
}

func TestFallback_NoRedis_UsesLocal(t *testing.T) {
	clk := clock.NewFake(time.Unix(1000, 0))
	f := NewFallbackFailureCounter(nil, clk) // no REDIS_URL
	ctx := context.Background()
	for want := 1; want <= 3; want++ {
		if got, err := f.Increment(ctx, "a@x"); err != nil || got != want {
			t.Fatalf("nil-redis increment: got %d err %v, want %d", got, err, want)
		}
	}
	_ = f.Reset(ctx, "a@x")
	if got, _ := f.Increment(ctx, "a@x"); got != 1 {
		t.Errorf("post-reset: got %d, want 1", got)
	}
}

func TestFallback_HealthyRedis_ServesRemote(t *testing.T) {
	clk := clock.NewFake(time.Unix(1000, 0))
	rem := &fakeRemote{}
	f := newFallbackWith(rem, clk)
	ctx := context.Background()
	for want := 1; want <= 3; want++ {
		if got, _ := f.Increment(ctx, "a@x"); got != want {
			t.Fatalf("healthy remote: got %d, want %d", got, want)
		}
	}
	if rem.calls != 3 {
		t.Errorf("remote should serve every call: calls=%d, want 3", rem.calls)
	}
}

func TestFallback_RedisError_FallsBackThenTripsBreaker(t *testing.T) {
	clk := clock.NewFake(time.Unix(1000, 0))
	rem := &fakeRemote{err: errors.New("redis down")}
	f := newFallbackWith(rem, clk)
	ctx := context.Background()

	// Each errored Increment must still return a real (in-memory) count, never 0/err.
	for want := 1; want <= failoverTripThreshold; want++ {
		got, err := f.Increment(ctx, "a@x")
		if err != nil || got != want {
			t.Fatalf("errored increment %d: got %d err %v (must fall back to local count)", want, got, err)
		}
	}
	callsAtTrip := rem.calls
	if callsAtTrip != failoverTripThreshold {
		t.Fatalf("remote calls before trip: got %d, want %d", callsAtTrip, failoverTripThreshold)
	}

	// Breaker now open — subsequent Increments must NOT touch the dead remote.
	_, _ = f.Increment(ctx, "a@x")
	_, _ = f.Increment(ctx, "a@x")
	if rem.calls != callsAtTrip {
		t.Errorf("breaker open should skip remote: calls=%d, want %d", rem.calls, callsAtTrip)
	}

	// After half-open window, it probes the remote again.
	clk.Advance(failoverHalfOpenAfter + time.Second)
	_, _ = f.Increment(ctx, "a@x")
	if rem.calls != callsAtTrip+1 {
		t.Errorf("half-open should retry remote once: calls=%d, want %d", rem.calls, callsAtTrip+1)
	}
}

func TestFallback_RedisRecovers_ClosesBreaker(t *testing.T) {
	clk := clock.NewFake(time.Unix(1000, 0))
	rem := &fakeRemote{err: errors.New("down")}
	f := newFallbackWith(rem, clk)
	ctx := context.Background()
	for i := 0; i < failoverTripThreshold; i++ {
		_, _ = f.Increment(ctx, "a@x") // trips breaker
	}
	clk.Advance(failoverHalfOpenAfter + time.Second)
	rem.mu.Lock()
	rem.err = nil // redis healthy again
	rem.mu.Unlock()
	_, _ = f.Increment(ctx, "a@x") // half-open probe succeeds → closes
	before := rem.calls
	_, _ = f.Increment(ctx, "a@x") // should hit remote (breaker closed)
	if rem.calls != before+1 {
		t.Errorf("after recovery, remote should serve: calls delta %d, want 1", rem.calls-before)
	}
}

func TestFallback_Reset_ClearsBoth(t *testing.T) {
	clk := clock.NewFake(time.Unix(1000, 0))
	rem := &fakeRemote{}
	f := newFallbackWith(rem, clk)
	ctx := context.Background()
	// Seed BOTH stores so the test proves Reset clears each (the healthy
	// path never touches local on its own, so seed local explicitly).
	_, _ = rem.Increment(ctx, "a@x")
	_, _ = f.local.Increment(ctx, "a@x")
	_ = f.Reset(ctx, "a@x")
	if rem.resets != 1 {
		t.Errorf("remote not reset: resets=%d", rem.resets)
	}
	if got, _ := f.local.Increment(ctx, "a@x"); got != 1 {
		t.Errorf("local not reset: got %d, want 1 (count carried over)", got)
	}
}

// ---- Service.RecordFailedAttempt (lockout decision) -----------------------

// lockoutStore records Lock calls and serves a configurable user.
type lockoutStore struct {
	user      domain.User
	hasUser   bool
	lockedID  string
	lockUntil time.Time
	lockCalls int
}

func (s *lockoutStore) Create(context.Context, string, string) (domain.User, error) {
	return domain.User{}, errs.ErrNotFound
}
func (s *lockoutStore) GetByEmail(_ context.Context, _ string) (domain.User, error) {
	if s.hasUser {
		return s.user, nil
	}
	return domain.User{}, errs.ErrNotFound
}
func (s *lockoutStore) GetByID(context.Context, string) (domain.User, error) {
	return domain.User{}, errs.ErrNotFound
}
func (s *lockoutStore) TouchLastLogin(context.Context, string, time.Time) error { return nil }
func (s *lockoutStore) Lock(_ context.Context, id string, until time.Time) error {
	s.lockCalls++
	s.lockedID = id
	s.lockUntil = until
	return nil
}
func (s *lockoutStore) SetPassword(context.Context, string, string) error { return nil }
func (s *lockoutStore) AttachTenant(context.Context, string, string, string) error {
	return nil
}
func (s *lockoutStore) TenantsForUser(context.Context, string) ([]domain.UserTenant, error) {
	return nil, nil
}
func (s *lockoutStore) CreateResetToken(context.Context, string, string, time.Time) (domain.PasswordResetToken, error) {
	return domain.PasswordResetToken{}, nil
}
func (s *lockoutStore) ConsumeResetToken(context.Context, string) (string, error) {
	return "", errs.ErrNotFound
}
func (s *lockoutStore) LookupResetToken(context.Context, string) (string, error) {
	return "", errs.ErrNotFound
}

func TestRecordFailedAttempt_LocksAfterThreshold_NoRedis(t *testing.T) {
	clk := clock.NewFake(time.Unix(1_700_000_000, 0))
	store := &lockoutStore{user: domain.User{ID: "usr_1"}, hasUser: true}
	svc := NewService(store, clk)
	svc.SetFailureCounter(NewFallbackFailureCounter(nil, clk)) // no Redis
	ctx := context.Background()

	for i := 0; i < FailedLoginThreshold-1; i++ {
		svc.RecordFailedAttempt(ctx, "victim@x.com")
		if store.lockCalls != 0 {
			t.Fatalf("locked early at attempt %d", i+1)
		}
	}
	svc.RecordFailedAttempt(ctx, "victim@x.com") // crosses threshold
	if store.lockCalls != 1 {
		t.Fatalf("lock calls: got %d, want 1 (lockout must fire even without Redis — #21)", store.lockCalls)
	}
	if store.lockedID != "usr_1" {
		t.Errorf("locked id: got %q, want usr_1", store.lockedID)
	}
	if want := clk.Now(ctx).Add(LockoutDuration); !store.lockUntil.Equal(want) {
		t.Errorf("locked_until: got %v, want %v", store.lockUntil, want)
	}
}

func TestRecordFailedAttempt_LocksDespiteRedisError(t *testing.T) {
	clk := clock.NewFake(time.Unix(1_700_000_000, 0))
	store := &lockoutStore{user: domain.User{ID: "usr_2"}, hasUser: true}
	svc := NewService(store, clk)
	svc.SetFailureCounter(newFallbackWith(&fakeRemote{err: errors.New("redis blip")}, clk))
	ctx := context.Background()
	for i := 0; i < FailedLoginThreshold; i++ {
		svc.RecordFailedAttempt(ctx, "victim@x.com")
	}
	if store.lockCalls != 1 {
		t.Errorf("lock calls under Redis error: got %d, want 1 (in-memory fallback must still reach threshold — #21 prod blip)", store.lockCalls)
	}
}

func TestRecordFailedAttempt_BelowThreshold_NoLock(t *testing.T) {
	clk := clock.NewFake(time.Unix(1_700_000_000, 0))
	store := &lockoutStore{user: domain.User{ID: "usr_3"}, hasUser: true}
	svc := NewService(store, clk)
	svc.SetFailureCounter(NewFallbackFailureCounter(nil, clk))
	ctx := context.Background()
	for i := 0; i < FailedLoginThreshold-1; i++ {
		svc.RecordFailedAttempt(ctx, "victim@x.com")
	}
	if store.lockCalls != 0 {
		t.Errorf("below threshold must not lock: got %d", store.lockCalls)
	}
}

func TestRecordFailedAttempt_UnknownEmail_NoLockNoPanic(t *testing.T) {
	clk := clock.NewFake(time.Unix(1_700_000_000, 0))
	store := &lockoutStore{hasUser: false} // GetByEmail misses
	svc := NewService(store, clk)
	svc.SetFailureCounter(NewFallbackFailureCounter(nil, clk))
	ctx := context.Background()
	for i := 0; i < FailedLoginThreshold+2; i++ {
		svc.RecordFailedAttempt(ctx, "ghost@x.com") // must not panic
	}
	if store.lockCalls != 0 {
		t.Errorf("non-existent email must not lock: got %d", store.lockCalls)
	}
}
