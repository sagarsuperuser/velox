// Package session implements dashboard sessions minted from a pasted
// API key. The credential is the API key (durable, operator-managed);
// the browser-side artefact is an httpOnly cookie tied to that key —
// see ADR-008 for why this shape was chosen over Bearer-in-localStorage.
//
// The package deliberately exposes only the surface the dashboard auth
// flow needs: Issue (mint a session from a validated key), Get (resolve
// a cookie value to its session row), Revoke (logout). Everything is
// keyed by sha256(raw_id); the raw id only ever lives in the cookie.
package session

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"
)

// CookieName is the HTTP cookie that carries the raw session id. Kept
// constant so middleware, login, and logout all reference the same
// name without risk of drift.
const CookieName = "velox_session"

// DefaultTTL is the default session lifetime. Shorter than typical
// password-based sessions (which were 30d) because the underlying
// credential is a durable API key — the session is just the
// browser-side artefact of "I pasted that key recently."
const DefaultTTL = 7 * 24 * time.Hour

// ErrNotFound signals that a session id resolves to no row, or to a
// row that has been revoked or expired. Callers funnel all three into
// 401 to deny enumeration of session ids.
var ErrNotFound = errors.New("session: not found")

// Session is the domain row. id_hash is sha256(raw); the raw id never
// leaves the cookie. Livemode is captured at session-issue time from
// the parent key — sessions don't toggle modes; a mode flip would mint
// a new session from the relevant key.
type Session struct {
	IDHash      string
	KeyID       string
	TenantID    string
	Livemode    bool
	CreatedAt   time.Time
	LastSeenAt  time.Time
	ExpiresAt   time.Time
	RevokedAt   *time.Time
	UserAgent   string
	IP          string
}

// IsActive reports whether the session can authenticate a request:
// not revoked and not yet expired. The DB query already filters
// revoked rows out for the active path; this guard catches expiry
// without a clock-keyed index lookup on every request.
func (s Session) IsActive(now time.Time) bool {
	if s.RevokedAt != nil {
		return false
	}
	return now.Before(s.ExpiresAt)
}

// HashID returns the storage form of a raw session id. Exported so
// the logout handler can hash the cookie value before calling Revoke.
func HashID(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// newRawID mints a 256-bit random id encoded as 64 hex chars. The raw
// form lives only in the cookie; the DB stores HashID(raw).
func newRawID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
