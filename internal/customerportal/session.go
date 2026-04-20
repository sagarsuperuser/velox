// Package customerportal authenticates requests from a customer (not a
// tenant operator) against the /v1/me/* surface using short-lived bearer
// tokens. The tenant operator mints a session for a specific customer via
// POST /v1/customer-portal-sessions, hands the returned URL to the
// customer, and from then on the customer can hit /v1/me/* without knowing
// any API key.
//
// The pattern mirrors payment_update_tokens (invoice-scoped, single-use)
// but is customer-scoped and reusable within TTL — a customer typically
// browses multiple pages in one portal session.
package customerportal

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// Session represents a live portal credential. The raw token is NEVER
// stored alongside the row — Create returns it once; subsequent lookups
// rehash the presented token and match on token_hash.
type Session struct {
	ID         string
	TenantID   string
	Livemode   bool
	CustomerID string
	ExpiresAt  time.Time
	RevokedAt  *time.Time
	CreatedAt  time.Time
}

// IsActive reports whether the session is currently usable — not revoked
// and not past its expiry. Store methods already filter on these; this
// helper is for callers holding a Session value (e.g. middleware).
func (s Session) IsActive(now time.Time) bool {
	if s.RevokedAt != nil {
		return false
	}
	return now.Before(s.ExpiresAt)
}

// DefaultTTL is the lifetime applied when Service.Create is called without
// a TTL override. 1h is long enough for a customer to add a card and pay
// an invoice in one sitting, short enough to limit exposure if the URL
// leaks.
const DefaultTTL = time.Hour

// tokenPrefix is included in every raw token so operators can visually
// distinguish portal tokens from API keys / update tokens in logs.
const tokenPrefix = "vlx_cps_"

// newToken generates a fresh random portal token and returns both the raw
// form (to hand to the customer) and its sha256 hash (to persist).
func newToken() (raw, hash string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	raw = tokenPrefix + hex.EncodeToString(buf)
	sum := sha256.Sum256([]byte(raw))
	hash = hex.EncodeToString(sum[:])
	return raw, hash, nil
}

// hashToken computes the stored hash of a presented token. Used by
// validation paths; isolated so both production and tests go through the
// same function and can't drift.
func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
