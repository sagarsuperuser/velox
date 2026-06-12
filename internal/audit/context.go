package audit

import (
	"context"
	"net"
	"net/http"
	"sync/atomic"
)

type ctxKey int

const (
	stateKey ctxKey = iota + 1
	ipKey
)

// requestState lets a handler signal the AuditLog middleware to suppress its
// catch-all write. Two reasons set it:
//   - MarkHandled: the handler wrote its own richer audit row, so the generic
//     middleware row would be a duplicate.
//   - MarkSkip: the request performs no auditable mutation (a read-only POST
//     such as an invoice or recipe *preview*), so there is nothing to audit at
//     all — like a GET, which the middleware already bypasses.
//
// Kept as a pointer so the mutation is visible back to the middleware frame
// through the ctx value lookup.
type requestState struct {
	suppressed atomic.Bool
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
		s.suppressed.Store(true)
	}
}

// MarkSkip records that this request performs no auditable mutation — e.g. a
// read-only POST such as an invoice or recipe *preview* that reads data but
// writes nothing. The AuditLog middleware otherwise classifies every
// successful non-GET request as a mutation (POST→create), which would record
// a spurious row (and, for invoices, a "View" link that 405s on the
// POST-only create_preview route). Read-only handlers call this so the
// catch-all write is skipped, exactly as it is for GET.
func MarkSkip(ctx context.Context) {
	if s, ok := ctx.Value(stateKey).(*requestState); ok {
		s.suppressed.Store(true)
	}
}

// WasHandled reports whether the catch-all middleware write should be
// suppressed — because a handler wrote its own row (MarkHandled) or opted out
// as non-mutating (MarkSkip).
func WasHandled(ctx context.Context) bool {
	if s, ok := ctx.Value(stateKey).(*requestState); ok {
		return s.suppressed.Load()
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

// ExtractClientIP returns the source IP recorded on audit rows: the host of
// r.RemoteAddr, which the global TrustedRealIP middleware has already resolved
// from X-Forwarded-For / X-Real-IP — but ONLY when the immediate peer is a
// configured trusted proxy. It deliberately does NOT re-read those headers
// itself: doing so trusted them unconditionally, so any client could forge
// audit_log.ip_address (and the boot log even claims headers are ignored when
// TRUST_PROXY is unset). Deferring to TrustedRealIP makes the audit IP match
// the real-IP policy the rest of the stack (rate limiter, logs) already uses.
func ExtractClientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
