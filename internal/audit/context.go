package audit

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
)

type ctxKey int

const (
	stateKey ctxKey = iota + 1
	ipKey
)

// requestState lets a handler that makes an explicit Log call signal the
// AuditLog middleware to suppress its catch-all write — otherwise a single
// user action produces two audit rows (one generic from the middleware, one
// rich from the handler). Kept as a pointer so the mutation is visible back
// to the middleware frame through the ctx value lookup.
type requestState struct {
	handled atomic.Bool
}

// WithRequestState seeds a fresh bookkeeping cell on the request context.
// Only the AuditLog middleware should call this; MarkHandled on a ctx that
// hasn't been seeded is a no-op (correct for background jobs / CLI callers).
func WithRequestState(ctx context.Context) context.Context {
	return context.WithValue(ctx, stateKey, &requestState{})
}

// MarkHandled records that an explicit audit write has occurred on this
// request. The AuditLog middleware checks this to avoid a duplicate row.
func MarkHandled(ctx context.Context) {
	if s, ok := ctx.Value(stateKey).(*requestState); ok {
		s.handled.Store(true)
	}
}

// WasHandled reports whether MarkHandled has been called on this ctx.
func WasHandled(ctx context.Context) bool {
	if s, ok := ctx.Value(stateKey).(*requestState); ok {
		return s.handled.Load()
	}
	return false
}

// WithClientIP stashes the request's source IP so downstream audit writes
// (middleware or explicit Logger.Log) record the same value.
func WithClientIP(ctx context.Context, ip string) context.Context {
	return context.WithValue(ctx, ipKey, ip)
}

// ClientIP returns the IP stashed by WithClientIP, or "" if none.
func ClientIP(ctx context.Context) string {
	if ip, ok := ctx.Value(ipKey).(string); ok {
		return ip
	}
	return ""
}

// ExtractClientIP picks the best-available source-IP string from an HTTP
// request: first entry of X-Forwarded-For, then X-Real-IP, then RemoteAddr
// with any port stripped. Trusts proxy headers unconditionally — the server
// is expected to run behind a known L7 proxy that strips client-supplied
// copies.
func ExtractClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.Index(xff, ","); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
