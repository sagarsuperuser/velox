package session

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/auth"
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

// Handler wires the dashboard auth endpoints. The credential is an
// API key the operator pasted on /login; the response is an httpOnly
// cookie tied to that key — see ADR-008 for the design.
type Handler struct {
	keys     *auth.Service
	sessions *Service
	cookie   CookieConfig
}

// NewHandler wires the dependencies. Both services are required.
func NewHandler(keys *auth.Service, sessions *Service, cookie CookieConfig) *Handler {
	return &Handler{keys: keys, sessions: sessions, cookie: cookie}
}

// Routes returns the public dashboard auth surface. Mount under
// /v1/auth. Both routes are intentionally outside session middleware:
// /exchange is pre-session, /logout takes the cookie via r.Cookie
// directly so a stale cookie doesn't 401 the very call that's
// supposed to revoke it.
func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/exchange", h.exchange)
	r.Post("/logout", h.logout)
	return r
}

type exchangeReq struct {
	APIKey string `json:"api_key"`
}

type exchangeResp struct {
	TenantID  string    `json:"tenant_id"`
	KeyID     string    `json:"key_id"`
	KeyType   string    `json:"key_type"`
	Livemode  bool      `json:"livemode"`
	ExpiresAt time.Time `json:"expires_at"`
}

// exchange validates a pasted API key, mints a session row, and sets
// the httpOnly cookie. Response body returns the resolved context so
// the dashboard can populate its AuthContext without a follow-up
// /v1/whoami round-trip.
//
// Error mapping deliberately collapses every credential failure into
// a single 401 with a single message — no enumeration of "key revoked"
// vs "key wrong format" vs "key expired" via the response body.
func (h *Handler) exchange(w http.ResponseWriter, r *http.Request) {
	var req exchangeReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}
	rawKey := strings.TrimSpace(req.APIKey)
	if rawKey == "" {
		respond.ValidationField(w, r, "api_key", "api_key is required")
		return
	}

	key, err := h.keys.ValidateKey(r.Context(), rawKey)
	if err != nil {
		respond.Unauthorized(w, r, "invalid or revoked API key")
		return
	}

	rawID, sess, err := h.sessions.Issue(r.Context(), IssueInput{
		KeyID:     key.ID,
		TenantID:  key.TenantID,
		Livemode:  key.Livemode,
		UserAgent: r.UserAgent(),
		IP:        clientIP(r),
	})
	if err != nil {
		slog.Error("session: issue failed", "err", err, "key_id", key.ID)
		respond.InternalError(w, r)
		return
	}

	h.setCookie(w, rawID, sess.ExpiresAt)
	respond.JSON(w, r, http.StatusOK, exchangeResp{
		TenantID:  key.TenantID,
		KeyID:     key.ID,
		KeyType:   string(key.KeyType),
		Livemode:  key.Livemode,
		ExpiresAt: sess.ExpiresAt,
	})
}

// logout revokes the session row matching the request's cookie and
// clears the cookie on the response. Idempotent — a missing or stale
// cookie is treated as already-logged-out.
func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(CookieName)
	if err == nil && c.Value != "" {
		if revokeErr := h.sessions.Revoke(r.Context(), c.Value); revokeErr != nil {
			// Don't fail the logout — the user's intent is to be
			// signed out client-side regardless of DB state. Log it
			// for ops; clear the cookie either way.
			slog.Error("session: revoke failed", "err", revokeErr)
		}
	}
	h.clearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) setCookie(w http.ResponseWriter, raw string, expires time.Time) {
	maxAge := int(time.Until(expires).Seconds())
	if maxAge < 0 {
		maxAge = 0
	}
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    raw,
		Path:     h.cookie.Path,
		Domain:   h.cookie.Domain,
		Expires:  expires,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   h.cookie.Secure,
		SameSite: h.cookie.SameSite,
	})
}

func (h *Handler) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     h.cookie.Path,
		Domain:   h.cookie.Domain,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   h.cookie.Secure,
		SameSite: h.cookie.SameSite,
	})
}

// clientIP pulls the caller's IP, preferring X-Forwarded-For (set by
// the RealIP middleware upstream). The stored value is informational
// — session validation isn't bound to IP for usability reasons (mobile
// network switching, VPN flips, etc.).
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if comma := strings.IndexByte(xff, ','); comma > 0 {
			return strings.TrimSpace(xff[:comma])
		}
		return strings.TrimSpace(xff)
	}
	return r.RemoteAddr
}
