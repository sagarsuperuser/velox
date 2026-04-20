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
	livemodeKey contextKey = "livemode"
)

// TestTenantIDKey returns the context key for tenant ID (for use in tests).
func TestTenantIDKey() contextKey { return tenantIDKey }

// Middleware validates API key credentials and injects tenant context.
func Middleware(svc *Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rawKey := extractBearerToken(r)
			if rawKey == "" {
				respond.Unauthorized(w, r, "missing credentials — use Authorization: Bearer vlx_secret_...")
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

// Livemode returns whether the request is operating in live mode. Absent a
// value, defaults to true — the safe fallback that matches production
// behavior and prevents background workers / tests from silently operating
// as if in test mode.
func Livemode(ctx context.Context) bool {
	v, ok := ctx.Value(livemodeKey).(bool)
	if !ok {
		return true
	}
	return v
}

// WithLivemode returns a derived context carrying the livemode flag. Used by
// auth middleware to propagate the key's mode downstream, and by tests to
// simulate test-mode requests.
func WithLivemode(ctx context.Context, live bool) context.Context {
	return context.WithValue(ctx, livemodeKey, live)
}

func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	return r.Header.Get("X-API-Key")
}
