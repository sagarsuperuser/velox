package middleware

import (
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// RateLimiter is a simple in-memory token bucket rate limiter.
// For production, swap this out for Redis-backed rate limiting.
type RateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rate    int           // requests per window
	window  time.Duration // window size
}

type bucket struct {
	tokens    int
	lastReset time.Time
}

// NewRateLimiter creates a rate limiter with the given rate per window.
// Example: NewRateLimiter(100, time.Minute) = 100 requests per minute.
func NewRateLimiter(rate int, window time.Duration) *RateLimiter {
	return &RateLimiter{
		buckets: make(map[string]*bucket),
		rate:    rate,
		window:  window,
	}
}

// Middleware returns chi-compatible rate limiting middleware.
// Keys by API key ID (from auth context) or IP address.
func (rl *RateLimiter) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip rate limiting for health checks
			if r.URL.Path == "/health" {
				next.ServeHTTP(w, r)
				return
			}

			key := rateLimitKey(r)
			remaining, resetAt := rl.allow(key)

			// Set rate limit headers (Stripe convention)
			w.Header().Set("X-RateLimit-Limit", strconv.Itoa(rl.rate))
			w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetAt.Unix(), 10))

			if remaining < 0 {
				w.Header().Set("Retry-After", strconv.Itoa(int(time.Until(resetAt).Seconds())+1))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				json.NewEncoder(w).Encode(map[string]any{
					"error": map[string]string{
						"type":    "rate_limit_error",
						"message": "Too many requests. Please retry after the rate limit resets.",
					},
				})
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func (rl *RateLimiter) allow(key string) (int, time.Time) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	b, ok := rl.buckets[key]
	if !ok || now.Sub(b.lastReset) >= rl.window {
		rl.buckets[key] = &bucket{tokens: rl.rate - 1, lastReset: now}
		return rl.rate - 1, now.Add(rl.window)
	}

	b.tokens--
	return b.tokens, b.lastReset.Add(rl.window)
}

func rateLimitKey(r *http.Request) string {
	// Use API key ID if authenticated
	if keyID := r.Context().Value("api_key_id"); keyID != nil {
		if id, ok := keyID.(string); ok && id != "" {
			return "key:" + id
		}
	}
	// Fallback to IP
	return "ip:" + r.RemoteAddr
}
