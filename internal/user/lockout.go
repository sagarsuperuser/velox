package user

import (
	"context"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"
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
