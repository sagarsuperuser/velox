package middleware

import (
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/go-redis/redis_rate/v10"
	goredis "github.com/redis/go-redis/v9"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/auth"
)

// RateLimiter implements distributed rate limiting using the GCRA (Generic Cell
// Rate Algorithm) backed by Redis. GCRA is a leaky-bucket variant that smooths
// traffic instead of allowing burst-then-block at window boundaries.
//
// Powered by go-redis/redis_rate — the official rate limiting companion for
// go-redis, used across the go-redis ecosystem.
type RateLimiter struct {
	limiter    *redis_rate.Limiter
	limit      redis_rate.Limit
	rdb        *goredis.Client
	failClosed bool
}

// SetFailClosed controls what happens when Redis is unreachable or unconfigured.
// Default (false) — fail open: allow all requests. Appropriate for local/dev.
// true — fail closed: return 429 for every non-infra request. Use in production,
// where availability without rate limiting is a DDoS vector.
func (rl *RateLimiter) SetFailClosed(v bool) {
	rl.failClosed = v
}

// NewRateLimiter creates a Redis-backed GCRA rate limiter.
// Example: NewRateLimiter(rdb, 100, time.Minute) = 100 requests per minute
// with smooth distribution (no boundary bursts).
// If rdb is nil, the limiter fails open (all requests allowed).
func NewRateLimiter(rdb *goredis.Client, rate int, window time.Duration) *RateLimiter {
	rl := &RateLimiter{rdb: rdb}

	// Convert rate + window to redis_rate.Limit
	switch {
	case window <= time.Second:
		rl.limit = redis_rate.PerSecond(rate)
	case window <= time.Minute:
		rl.limit = redis_rate.PerMinute(rate)
	default:
		rl.limit = redis_rate.Limit{
			Rate:   rate,
			Burst:  rate, // allow full rate as burst capacity
			Period: window,
		}
	}

	if rdb != nil {
		rl.limiter = redis_rate.NewLimiter(rdb)
	}

	return rl
}

// Middleware returns chi-compatible rate limiting middleware.
// Keys by tenant ID (from auth context) or IP address for unauthenticated requests.
func (rl *RateLimiter) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip rate limiting for infrastructure endpoints
			if r.URL.Path == "/health" || r.URL.Path == "/health/ready" || r.URL.Path == "/metrics" {
				next.ServeHTTP(w, r)
				return
			}

			key := rateLimitKey(r)
			remaining, resetAt, allowed := rl.allow(r, key)

			// Set rate limit headers (Stripe convention)
			w.Header().Set("X-RateLimit-Limit", strconv.Itoa(rl.limit.Rate))
			w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetAt.Unix(), 10))

			if !allowed {
				retryAfter := int(time.Until(resetAt).Seconds()) + 1
				if retryAfter < 1 {
					retryAfter = 1
				}
				w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
				respond.RateLimited(w, r)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// allow checks the rate limit for the given key.
// Returns (remaining, resetAt, allowed). On Redis absence or error, behavior
// depends on failClosed — see SetFailClosed.
func (rl *RateLimiter) allow(r *http.Request, key string) (int, time.Time, bool) {
	now := time.Now().UTC()

	if rl.limiter == nil {
		if rl.failClosed {
			slog.Error("rate_limiter: no redis configured, failing closed", "key", key)
			return 0, now.Add(rl.limit.Period), false
		}
		return rl.limit.Rate - 1, now.Add(rl.limit.Period), true
	}

	res, err := rl.limiter.Allow(r.Context(), "rl:"+key, rl.limit)
	if err != nil {
		if rl.failClosed {
			slog.Error("rate_limiter: redis error, failing closed",
				"error", err,
				"key", key,
			)
			return 0, now.Add(rl.limit.Period), false
		}
		slog.Warn("rate_limiter: redis error, failing open",
			"error", err,
			"key", key,
		)
		return rl.limit.Rate - 1, now.Add(rl.limit.Period), true
	}

	resetAt := now.Add(res.ResetAfter)
	return res.Remaining, resetAt, res.Allowed > 0
}

func rateLimitKey(r *http.Request) string {
	// Prefer tenant-scoped bucket so all keys for the same tenant share a limit
	if tenantID := auth.TenantID(r.Context()); tenantID != "" {
		return "tenant:" + tenantID
	}
	// Fallback to IP for unauthenticated requests (strip port)
	ip := r.RemoteAddr
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	return "ip:" + ip
}
