package session

import (
	"net/http"
	"os"
	"strings"
	"time"
)

// CookieConfig centralises cookie attributes. Secure is pinned by
// APP_ENV: off for local (HTTP), on for staging/production (HTTPS).
// SameSite=Lax keeps the cookie attached across top-level navigation
// (correct for a first-party dashboard) while blocking most cross-site
// CSRF.
type CookieConfig struct {
	Domain   string
	Secure   bool
	SameSite http.SameSite
	Path     string
}

// DefaultCookieConfig reads APP_ENV and returns a sensible default.
// Tests override fields directly.
func DefaultCookieConfig() CookieConfig {
	env := strings.ToLower(strings.TrimSpace(os.Getenv("APP_ENV")))
	secure := env == "production" || env == "staging"
	return CookieConfig{
		Domain:   strings.TrimSpace(os.Getenv("VELOX_COOKIE_DOMAIN")),
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		Path:     "/",
	}
}

// SetCookie writes the session cookie on the response. Exported so the
// auth login handler in internal/userauth can call it after Issue
// without depending on internals here.
func (c CookieConfig) SetCookie(w http.ResponseWriter, raw string, expires time.Time) {
	maxAge := int(time.Until(expires).Seconds())
	if maxAge < 0 {
		maxAge = 0
	}
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    raw,
		Path:     c.Path,
		Domain:   c.Domain,
		Expires:  expires,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   c.Secure,
		SameSite: c.SameSite,
	})
}

// ClearCookie writes a cleared (Max-Age=-1) cookie on the response.
// Used by the logout handler.
func (c CookieConfig) ClearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     c.Path,
		Domain:   c.Domain,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   c.Secure,
		SameSite: c.SameSite,
	})
}

// ClientIP pulls the caller's IP, preferring X-Forwarded-For (set by
// the RealIP middleware upstream). The stored value is informational
// — session validation isn't bound to IP for usability reasons (mobile
// network switching, VPN flips, etc.).
func ClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if comma := strings.IndexByte(xff, ','); comma > 0 {
			return strings.TrimSpace(xff[:comma])
		}
		return strings.TrimSpace(xff)
	}
	return r.RemoteAddr
}
