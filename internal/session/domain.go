package session

import (
	"errors"
	"time"
)

// DefaultTTL is how long a fresh session stays valid without activity.
// 30 days is the usual dashboard sweet spot — long enough that a weekly
// user never sees a forced logout, short enough that a stolen cookie
// stops working within a reasonable blast-radius window.
const DefaultTTL = 30 * 24 * time.Hour

// CookieName is the HTTP cookie that carries the raw session ID. Kept
// deliberately opaque (no product hint) — the cookie's purpose is not
// marketing.
const CookieName = "velox_session"

// Session is the shape served to callers. The DB stores id_hash, not the
// raw ID; Session.ID here is always the raw form (returned only once, at
// issue time, for the caller to write into the cookie).
type Session struct {
	ID         string // raw, present only at issue time
	IDHash     string
	UserID     string
	TenantID   string
	Livemode   bool
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
	RevokedAt  *time.Time
	UserAgent  string
	IP         string
}

// Active reports whether a session row is currently usable: not revoked,
// not expired. Middleware uses this as the final gate before setting ctx.
func (s Session) Active(now time.Time) bool {
	if s.RevokedAt != nil {
		return false
	}
	return now.Before(s.ExpiresAt)
}

var (
	ErrNotFound = errors.New("session: not found")
	ErrRevoked  = errors.New("session: revoked")
	ErrExpired  = errors.New("session: expired")
)
