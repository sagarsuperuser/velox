package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/sagarsuperuser/velox/internal/api/respond"
)

type contextKey string

const (
	tenantIDKey contextKey = "tenant_id"
	apiKeyIDKey contextKey = "api_key_id"
	keyTypeKey  contextKey = "key_type"
	userIDKey   contextKey = "user_id"
)

// TestTenantIDKey returns the context key for tenant ID (for use in tests).
func TestTenantIDKey() contextKey { return tenantIDKey }

// SessionValidator is the interface the middleware uses to validate session cookies.
// Implemented by userauth.Service to avoid a circular import.
type SessionValidator interface {
	// ValidateSession checks a session token and returns (userID, tenantID, error).
	ValidateSessionForAuth(ctx context.Context, token string) (userID, tenantID string, err error)
}

// Middleware validates auth credentials and injects tenant context.
// It checks for a session cookie first, then falls back to API key.
func Middleware(svc *Service, sessions ...SessionValidator) func(http.Handler) http.Handler {
	var sessionValidator SessionValidator
	if len(sessions) > 0 {
		sessionValidator = sessions[0]
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 1. Try session cookie first (dashboard users)
			if sessionValidator != nil {
				if cookie, err := r.Cookie("velox_session"); err == nil && cookie.Value != "" {
					userID, tenantID, err := sessionValidator.ValidateSessionForAuth(r.Context(), cookie.Value)
					if err == nil && tenantID != "" {
						ctx := r.Context()
						ctx = context.WithValue(ctx, tenantIDKey, tenantID)
						ctx = context.WithValue(ctx, userIDKey, userID)
						// Session users get secret-level permissions (full dashboard access)
						ctx = context.WithValue(ctx, keyTypeKey, KeyTypeSecret)
						next.ServeHTTP(w, r.WithContext(ctx))
						return
					}
					// Invalid/expired cookie — fall through to API key check
				}
			}

			// 2. Fall back to API key (external API consumers)
			rawKey := extractBearerToken(r)
			if rawKey == "" {
				respond.Unauthorized(w, r, "missing credentials — use a session cookie or Authorization: Bearer vlx_secret_...")
				return
			}

			key, err := svc.ValidateKey(r.Context(), rawKey)
			if err != nil {
				respond.Unauthorized(w, r, err.Error())
				return
			}

			ctx := r.Context()
			ctx = context.WithValue(ctx, tenantIDKey, key.TenantID)
			ctx = context.WithValue(ctx, apiKeyIDKey, key.ID)
			ctx = context.WithValue(ctx, keyTypeKey, KeyType(key.KeyType))

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// UserID returns the authenticated user ID from context (session auth only).
func UserID(ctx context.Context) string {
	v, _ := ctx.Value(userIDKey).(string)
	return v
}

// Require returns middleware that checks if the authenticated key has a specific permission.
// Use this to protect individual route groups.
func Require(perm Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			kt := GetKeyType(r.Context())
			if !HasPermission(kt, perm) {
				respond.Forbidden(w, r,
					"insufficient permissions: this key type does not have "+string(perm)+" access")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// Context accessors

func TenantID(ctx context.Context) string {
	v, _ := ctx.Value(tenantIDKey).(string)
	return v
}

func KeyID(ctx context.Context) string {
	v, _ := ctx.Value(apiKeyIDKey).(string)
	return v
}

func GetKeyType(ctx context.Context) KeyType {
	v, _ := ctx.Value(keyTypeKey).(KeyType)
	return v
}

func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	return r.Header.Get("X-API-Key")
}
