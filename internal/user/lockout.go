package user

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/sagarsuperuser/velox/internal/platform/clock"
)

// RedisFailureCounter is the production FailureCounter implementation,
// backed by a single INCR + EXPIRE per failed login. Email is
// lower-cased + trimmed before keying so "Alice@x.com" and "alice@x.com"
// share the same counter. TTL = LockoutDuration so a user who fails 4
// times then waits has the count auto-reset before their next attempt
// — same shape as GitHub / Stripe automatic-lockout backoff.
type RedisFailureCounter struct {
	rdb *goredis.Client
	// keyPrefix isolates these counters from other Redis usage in the
	// same instance (rate limiter buckets, idempotency cache, etc.).
	keyPrefix string
}

func NewRedisFailureCounter(rdb *goredis.Client) *RedisFailureCounter {
	return &RedisFailureCounter{rdb: rdb, keyPrefix: "velox:login_fail:"}
}

func (c *RedisFailureCounter) key(email string) string {
	return c.keyPrefix + strings.ToLower(strings.TrimSpace(email))
}

func (c *RedisFailureCounter) Increment(ctx context.Context, email string) (int, error) {
	if c == nil || c.rdb == nil {
		return 0, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	k := c.key(email)
	pipe := c.rdb.Pipeline()
	incr := pipe.Incr(ctx, k)
	// Set TTL only on the first INCR (when value transitions 0→1) so
	// a fast-firing attacker can't refresh the window indefinitely by
	// re-touching the key. EXPIRE NX (v7+) does this atomically; the
	// older EXPIRE-only approach can race with concurrent Increments
	// resetting TTL just before the lockout would have triggered.
	pipe.ExpireNX(ctx, k, LockoutDuration)
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, err
	}
	return int(incr.Val()), nil
}

func (c *RedisFailureCounter) Reset(ctx context.Context, email string) error {
	if c == nil || c.rdb == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	return c.rdb.Del(ctx, c.key(email)).Err()
}

// memEntry is one email's in-memory failed-login count + its window expiry.
type memEntry struct {
	n         int
	expiresAt time.Time
}

// memFailureCounter is a process-local FailureCounter. It is the degraded
// fallback the lockout uses when Redis is unavailable or unconfigured, so
// brute-force protection on /v1/auth/login never silently evaporates
// (velox-ops #21). Per-process state, so behind N instances the effective
// global limit during a Redis outage is ~FailedLoginThreshold*N — bounded,
// OWASP-ASVS-acceptable degradation, vastly better than the prior unbounded
// fail-open. The lockout DECISION (users.locked_until in Postgres) is
// unaffected; this only restores the COUNTER that a store outage was wiping.
type memFailureCounter struct {
	mu     sync.Mutex
	counts map[string]memEntry
	clk    clock.Clock

	lastSwept time.Time // last time the whole-map prune ran (see maybeSweep)
	sweepRuns int       // count of full prunes; observability + test seam
}

func newMemFailureCounter(clk clock.Clock) *memFailureCounter {
	if clk == nil {
		clk = clock.Real()
	}
	return &memFailureCounter{counts: make(map[string]memEntry), clk: clk}
}

func memKey(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

// memSweepInterval bounds how often the O(n) expired-window prune runs. The
// per-key window check on the hot path is already O(1) (an expired entry for
// the same email is reset on its next Increment regardless of the sweep), so
// the full-map sweep only reclaims memory for emails that aren't being
// re-tried. Throttling it to once per interval keeps a high-rate
// credential-stuffing flood from turning every login into a whole-map scan
// (velox-ops #21 adversarial review). 1 minute ≪ LockoutDuration, so the map
// can never hold more than ~(request-rate × 1 min) of stale entries.
const memSweepInterval = time.Minute

func (m *memFailureCounter) Increment(ctx context.Context, email string) (int, error) {
	now := m.clk.Now(ctx)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.maybeSweep(now)
	k := memKey(email)
	e := m.counts[k]
	if e.expiresAt.After(now) {
		e.n++
	} else {
		// New (or lapsed) window. Seed TTL only on the 0→1 transition so a fast
		// attacker can't push the window out by re-touching the key — mirrors the
		// Redis EXPIRE-NX semantics above. This per-key check is what enforces
		// expiry; maybeSweep is purely memory reclamation for other keys.
		e = memEntry{n: 1, expiresAt: now.Add(LockoutDuration)}
	}
	m.counts[k] = e
	return e.n, nil
}

// maybeSweep drops every expired window, but at most once per memSweepInterval
// so a flood of distinct emails can't make each Increment an O(n) scan. Caller
// holds m.mu. Between sweeps the map may carry expired entries; they cost a
// little memory but never affect correctness (each key's expiry is checked on
// read). Reclamation is request-driven — if login traffic stops entirely the
// stale entries persist until the next attempt, an accepted tradeoff: the set
// is bounded by the prior window's distinct-email count and a single later
// login reclaims all of it, so no unbounded growth and no idle goroutine.
func (m *memFailureCounter) maybeSweep(now time.Time) {
	if !m.lastSwept.IsZero() && now.Sub(m.lastSwept) < memSweepInterval {
		return
	}
	m.lastSwept = now
	m.sweepRuns++
	for k, e := range m.counts {
		if !e.expiresAt.After(now) {
			delete(m.counts, k)
		}
	}
}

func (m *memFailureCounter) Reset(_ context.Context, email string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.counts, memKey(email))
	return nil
}

const (
	// failoverTripThreshold consecutive Redis errors trip the breaker to the
	// in-memory counter; failoverHalfOpenAfter is how long it serves locally
	// before probing Redis again. Matches the common production rate-limiter
	// circuit-breaker shape.
	failoverTripThreshold = 3
	failoverHalfOpenAfter = 30 * time.Second
)

// FallbackFailureCounter is the production FailureCounter. It serves from the
// shared Redis counter when healthy, and transparently degrades to a
// process-local in-memory counter (with a circuit breaker so it stops hammering
// a dead Redis) on a Redis error or when Redis is unconfigured. This is the
// velox-ops #21 fix: the failed-login throttle stays enforced in every
// environment — local dev, staging, and during a production Redis blip —
// without the naive "fail closed → block every login" DoS the old code
// (correctly) rejected. The Postgres-backed lock (users.locked_until) is the
// source of truth and is untouched by Redis state, so an already-locked account
// never silently unlocks during an outage.
type FallbackFailureCounter struct {
	// remote is the shared (Redis) counter, or nil when REDIS_URL is unset.
	// Typed as the interface so tests can inject a fake that returns canned
	// values / errors; production always gets *RedisFailureCounter.
	remote FailureCounter
	local  *memFailureCounter
	clk    clock.Clock

	mu         sync.Mutex
	consecFail int
	openUntil  time.Time // breaker open (serve local-only) while now < openUntil
	warned     bool      // log the degrade once per trip, not every request
}

// NewFallbackFailureCounter wires the always-on failed-login counter. rdb may
// be nil (no REDIS_URL) — then it serves purely from the in-memory counter.
func NewFallbackFailureCounter(rdb *goredis.Client, clk clock.Clock) *FallbackFailureCounter {
	if clk == nil {
		clk = clock.Real()
	}
	var remote FailureCounter
	if rdb != nil {
		remote = NewRedisFailureCounter(rdb)
	}
	return &FallbackFailureCounter{remote: remote, local: newMemFailureCounter(clk), clk: clk}
}

func (f *FallbackFailureCounter) breakerOpen(now time.Time) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return now.Before(f.openUntil)
}

func (f *FallbackFailureCounter) recordRedisFailure(now time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.consecFail++
	if f.consecFail >= failoverTripThreshold {
		f.openUntil = now.Add(failoverHalfOpenAfter)
		if !f.warned {
			f.warned = true
			slog.Warn("lockout: Redis failed-login counter unavailable; serving from in-process counter (brute-force protection still enforced, per-instance)")
		}
	}
}

func (f *FallbackFailureCounter) recordRedisSuccess() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.consecFail = 0
	f.openUntil = time.Time{}
	f.warned = false
}

func (f *FallbackFailureCounter) Increment(ctx context.Context, email string) (int, error) {
	now := f.clk.Now(ctx)
	if f.remote == nil || f.breakerOpen(now) {
		return f.local.Increment(ctx, email)
	}
	n, err := f.remote.Increment(ctx, email)
	if err != nil {
		f.recordRedisFailure(now)
		return f.local.Increment(ctx, email)
	}
	f.recordRedisSuccess()
	return n, nil
}

func (f *FallbackFailureCounter) Reset(ctx context.Context, email string) error {
	// Clear BOTH stores so a successful login (or a triggered lockout) can't
	// leave a stale count on whichever path wasn't serving.
	_ = f.local.Reset(ctx, email)
	if f.remote != nil {
		_ = f.remote.Reset(ctx, email)
	}
	return nil
}
