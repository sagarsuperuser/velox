package session

import (
	"context"
	"net/http"
	"strings"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

// Middleware accepts a `velox_session` httpOnly cookie. On hit it
// resolves the session, projects the parent key's tenant context onto
// the request, and forwards. On miss it 401s without falling back to
// API-key auth — use MiddlewareOrAPIKey for routes that should accept
// either credential.
func Middleware(svc *Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := r.Cookie(CookieName)
			if err != nil || c.Value == "" {
				respond.Unauthorized(w, r, "missing session — sign in at /login")
				return
			}
			sess, err := svc.Resolve(r.Context(), c.Value)
			if err != nil {
				respond.Unauthorized(w, r, "invalid or expired session")
				return
			}
			next.ServeHTTP(w, r.WithContext(applyToCtx(r.Context(), sess)))
		})
	}
}

// MiddlewareOrAPIKey accepts either a session cookie OR an
// `Authorization: Bearer <api_key>` header. The dashboard rides the
// cookie path; SDKs and curl callers ride the API-key path. Cookie
// takes precedence when both are present so a browser tab with a
// stale Authorization header doesn't accidentally bypass session
// revocation.
func MiddlewareOrAPIKey(sessSvc *Service, keySvc *auth.Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if c, err := r.Cookie(CookieName); err == nil && c.Value != "" {
				if sess, err := sessSvc.Resolve(r.Context(), c.Value); err == nil {
					next.ServeHTTP(w, r.WithContext(applyToCtx(r.Context(), sess)))
					return
				}
				// Cookie present but invalid — fall through to API-key
				// rather than 401, so a stale cookie doesn't block a
				// valid Bearer header on the same request.
			}

			rawKey := extractBearer(r)
			if rawKey == "" {
				respond.Unauthorized(w, r, "missing credentials — sign in at /login or send Authorization: Bearer vlx_secret_...")
				return
			}
			key, err := keySvc.ValidateKey(r.Context(), rawKey)
			if err != nil {
				respond.Unauthorized(w, r, err.Error())
				return
			}
			ctx := r.Context()
			ctx = auth.WithTenantID(ctx, key.TenantID)
			ctx = withKeyID(ctx, key.ID)
			ctx = withKeyType(ctx, auth.KeyType(key.KeyType))
			ctx = postgres.WithLivemode(ctx, key.Livemode)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func applyToCtx(ctx context.Context, s Session) context.Context {
	ctx = auth.WithTenantID(ctx, s.TenantID)
	ctx = withKeyID(ctx, s.KeyID)
	// Session-authed requests inherit the parent key's permissions.
	// We don't carry KeyType on the session row to avoid drift if the
	// underlying key's type ever changes mid-session; resolving it
	// fresh from auth.Service would require a DB roundtrip on every
	// request, which is overkill for v1. The dashboard only mints
	// sessions from secret keys via /v1/auth/exchange, so KeyType
	// defaults to secret here — refine when publishable-key sessions
	// exist (see ADR-008 mitigations list).
	ctx = withKeyType(ctx, auth.KeyTypeSecret)
	ctx = postgres.WithLivemode(ctx, s.Livemode)
	return ctx
}

// withKeyID and withKeyType are local thin wrappers that route
// through auth.WithTenantID's siblings without re-exporting them. The
// auth package owns the canonical context keys; we just call its
// setters.
func withKeyID(ctx context.Context, id string) context.Context {
	return auth.WithKeyID(ctx, id)
}

func withKeyType(ctx context.Context, kt auth.KeyType) context.Context {
	return auth.WithKeyType(ctx, kt)
}

func extractBearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return r.Header.Get("X-API-Key")
}
