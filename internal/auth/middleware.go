package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

type contextKey string

const (
	tenantIDKey contextKey = "tenant_id"
	apiKeyIDKey contextKey = "api_key_id"
	keyTypeKey  contextKey = "key_type"
	userIDKey   contextKey = "user_id"
	sessionIDKey contextKey = "session_id"
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
			ctx = postgres.WithLivemode(ctx, key.Livemode)

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

// WithTenantID returns a derived context carrying tenantID. Middleware uses
// this indirectly via its own WithValue call; background workers (reconciler,
// billing engine charge loop) use this explicitly to seed the tenant ctx
// before calling Stripe-resolver-backed clients so per-tenant credentials
// can be looked up.
func WithTenantID(ctx context.Context, tenantID string) context.Context {
	return context.WithValue(ctx, tenantIDKey, tenantID)
}

func KeyID(ctx context.Context) string {
	v, _ := ctx.Value(apiKeyIDKey).(string)
	return v
}

func GetKeyType(ctx context.Context) KeyType {
	v, _ := ctx.Value(keyTypeKey).(KeyType)
	return v
}

// WithKeyType lets adapters outside the auth package (session middleware,
// future OIDC adapter) declare the principal type so auth.Require keeps
// working off a single ctx key. Called with KeyTypeSession by the session
// middleware.
func WithKeyType(ctx context.Context, kt KeyType) context.Context {
	return context.WithValue(ctx, keyTypeKey, kt)
}

// UserID is set by the session middleware when a request is authenticated
// via a dashboard cookie. Unset (empty string) on API-key-authenticated
// requests — domain code should treat empty as "no human user attached".
func UserID(ctx context.Context) string {
	v, _ := ctx.Value(userIDKey).(string)
	return v
}

// WithUserID is the setter counterpart to UserID.
func WithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, userIDKey, userID)
}

// SessionID returns the opaque session identifier for cookie-authenticated
// requests. Used by the mode-toggle endpoint to locate the row to update.
func SessionID(ctx context.Context) string {
	v, _ := ctx.Value(sessionIDKey).(string)
	return v
}

// WithSessionID is the setter counterpart to SessionID.
func WithSessionID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, sessionIDKey, id)
}

// Livemode returns whether the request is operating in live mode. Delegates
// to the shared postgres-package accessor so BeginTx and auth see the same
// value off the same ctx key.
func Livemode(ctx context.Context) bool {
	return postgres.Livemode(ctx)
}

// WithLivemode returns a derived context carrying the livemode flag. Used by
// auth middleware to propagate the key's mode downstream, and by tests to
// simulate test-mode requests.
func WithLivemode(ctx context.Context, live bool) context.Context {
	return postgres.WithLivemode(ctx, live)
}

func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	return r.Header.Get("X-API-Key")
}
