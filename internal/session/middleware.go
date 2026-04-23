package session

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

// MiddlewareOrAPIKey accepts either a dashboard session cookie OR an
// Authorization: Bearer API key, so the same `/v1/*` endpoints can be reached
// by the dashboard (cookie-authed) and by external integrations (API-key-authed).
//
// A session cookie takes precedence — if the cookie is present, session auth
// runs and the API-key path is not tried even on failure (otherwise an expired
// cookie would fall through to a second 401 with a misleading message).
// Missing cookie → API-key middleware runs, which returns its own 401 if the
// Authorization header is also absent.
//
// Mode-mismatch guard: when BOTH auth methods are present on the same request
// (cookie + API key) and they disagree on livemode, the request is rejected
// with 400. Without this, the cookie's mode silently wins and a developer
// holding a test key would unwittingly read live data (or vice versa) — a
// "did I just do that on live?" footgun. Stripe-grade: a request is in ONE
// mode, always.
//
// Both paths populate the same ctx keys (tenant_id, user_id, key_type,
// livemode), so downstream handlers are oblivious to which one ran.
func MiddlewareOrAPIKey(sessSvc *Service, keySvc *auth.Service) func(http.Handler) http.Handler {
	cookieMW := Middleware(sessSvc)
	keyMW := auth.Middleware(keySvc)
	return func(next http.Handler) http.Handler {
		// After cookie middleware populates ctx livemode, confirm any
		// Authorization-header key in the same request agrees.
		guardedNext := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if keyLive, ok := auth.LivemodeFromRequest(r); ok {
				sessLive := postgres.Livemode(r.Context())
				if keyLive != sessLive {
					respond.BadRequest(w, r,
						"auth mode mismatch: session and API key are in different modes — remove one or align them")
					return
				}
			}
			next.ServeHTTP(w, r)
		})
		cookieNext := cookieMW(guardedNext)
		keyNext := keyMW(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, err := r.Cookie(CookieName); err == nil {
				cookieNext.ServeHTTP(w, r)
				return
			}
			keyNext.ServeHTTP(w, r)
		})
	}
}

// Middleware authenticates a request by dashboard session cookie. On
// success it populates the same ctx keys as APIKeyAuth so downstream
// handlers (customer.handler, pricing.handler, …) don't need to know
// which auth path ran.
//
// Returns 401 if the cookie is missing, unknown, revoked, or expired.
// Intended to be mounted at route groups that serve the dashboard UI; the
// mode toggle is the first consumer.
func Middleware(svc *Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := r.Cookie(CookieName)
			if err != nil || c.Value == "" {
				respond.Unauthorized(w, r, "missing session cookie")
				return
			}
			sess, err := svc.Lookup(r.Context(), c.Value)
			if err != nil {
				switch {
				case errors.Is(err, ErrNotFound), errors.Is(err, ErrRevoked):
					respond.Unauthorized(w, r, "session not recognised — log in again")
				case errors.Is(err, ErrExpired):
					respond.Unauthorized(w, r, "session expired — log in again")
				default:
					respond.InternalError(w, r)
				}
				return
			}

			// Best-effort last_seen_at bump. A write failure here shouldn't
			// boot the user; we log and carry on.
			if err := svc.Touch(r.Context(), sess.IDHash); err != nil {
				slog.Warn("session touch failed", "err", err)
			}

			ctx := r.Context()
			ctx = auth.WithTenantID(ctx, sess.TenantID)
			ctx = auth.WithUserID(ctx, sess.UserID)
			ctx = auth.WithSessionID(ctx, sess.IDHash)
			ctx = auth.WithKeyType(ctx, auth.KeyTypeSession)
			ctx = postgres.WithLivemode(ctx, sess.Livemode)

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
