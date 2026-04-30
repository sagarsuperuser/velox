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

// RequireMethod returns middleware that picks the permission to enforce
// based on HTTP method: GET/HEAD/OPTIONS check `read`, POST/PUT/PATCH/
// DELETE check `write`. Closes the gap where a single Require() in
// front of a chi.Mount applies one permission to a subrouter that
// mixes reads and writes — left unguarded, a key with read access
// could write on every endpoint inside the subtree.
//
// Usage:
//
//	r.With(auth.RequireMethod(auth.PermCustomerRead, auth.PermCustomerWrite)).
//	    Mount("/customers", customerH.Routes())
func RequireMethod(read, write Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			perm := read
			switch r.Method {
			case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
				perm = write
			}
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

// WithKeyID is the setter counterpart to KeyID. Used by adapters
// outside the auth package (currently `internal/session`) that
// resolve a credential to a key context themselves.
func WithKeyID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, apiKeyIDKey, id)
}

func GetKeyType(ctx context.Context) KeyType {
	v, _ := ctx.Value(keyTypeKey).(KeyType)
	return v
}

// WithKeyType lets adapters declare the principal type so auth.Require
// keeps working off a single ctx key. Called by `internal/session`
// when a session resolves to its parent key's type.
func WithKeyType(ctx context.Context, kt KeyType) context.Context {
	return context.WithValue(ctx, keyTypeKey, kt)
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

// LivemodeFromRawKey peeks at a raw API key's prefix to recover the mode it
// was minted for, without touching the DB. Returns ok=false for strings that
// don't look like Velox API keys so callers can skip the check silently.
func LivemodeFromRawKey(raw string) (live, ok bool) {
	switch {
	case strings.Contains(raw, "_live_"):
		return true, true
	case strings.Contains(raw, "_test_"):
		return false, true
	}
	return false, false
}

// LivemodeFromRequest extracts the mode from whichever auth header the
// request carries (Authorization: Bearer or X-API-Key), if any. Returns
// ok=false when the request has no API key or the key prefix is unrecognised.
func LivemodeFromRequest(r *http.Request) (live, ok bool) {
	raw := extractBearerToken(r)
	if raw == "" {
		return false, false
	}
	return LivemodeFromRawKey(raw)
}
