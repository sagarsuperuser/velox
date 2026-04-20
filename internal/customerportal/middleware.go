package customerportal

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

type ctxKey string

const (
	customerIDKey ctxKey = "cp_customer_id"
	tenantIDKey   ctxKey = "cp_tenant_id"
	sessionIDKey  ctxKey = "cp_session_id"
)

// Middleware authenticates /v1/me/* requests via a portal bearer token.
// On success it injects the tenant_id, customer_id, session_id, and
// livemode into ctx — downstream handlers read these the same way the
// auth package handlers read tenant + livemode. On miss/expired/revoked
// it responds 401 with a generic message (no leakage of which).
//
// Note: this runs BEFORE any tenant RLS is established, because until the
// token resolves we don't know which tenant the request belongs to. The
// Service.Validate path runs under TxBypass for exactly this reason.
func (s *Service) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := extractBearer(r)
			if raw == "" {
				respond.Unauthorized(w, r, "missing credentials — use Authorization: Bearer vlx_cps_...")
				return
			}
			if !strings.HasPrefix(raw, tokenPrefix) {
				respond.Unauthorized(w, r, "invalid portal token")
				return
			}
			sess, err := s.Validate(r.Context(), raw)
			if err != nil {
				if errors.Is(err, errs.ErrNotFound) {
					respond.Unauthorized(w, r, "invalid or expired portal session")
					return
				}
				respond.InternalError(w, r)
				return
			}

			ctx := r.Context()
			ctx = context.WithValue(ctx, tenantIDKey, sess.TenantID)
			ctx = context.WithValue(ctx, customerIDKey, sess.CustomerID)
			ctx = context.WithValue(ctx, sessionIDKey, sess.ID)
			ctx = postgres.WithLivemode(ctx, sess.Livemode)

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// TenantID returns the tenant_id that Middleware injected. Empty if the
// request didn't pass through Middleware.
func TenantID(ctx context.Context) string {
	v, _ := ctx.Value(tenantIDKey).(string)
	return v
}

// CustomerID returns the customer_id that Middleware injected.
func CustomerID(ctx context.Context) string {
	v, _ := ctx.Value(customerIDKey).(string)
	return v
}

// SessionID returns the session_id that Middleware injected.
func SessionID(ctx context.Context) string {
	v, _ := ctx.Value(sessionIDKey).(string)
	return v
}

func extractBearer(r *http.Request) string {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return ""
	}
	return tok
}
