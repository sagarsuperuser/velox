package middleware

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

// maxIdempotentBodyBytes caps how much of the request body we hash into the
// fingerprint. Requests larger than this (e.g., bulk imports) skip the body
// portion of the fingerprint and fall back to method+path — those endpoints
// are rare and the risk of key reuse with different bodies is low.
const maxIdempotentBodyBytes = 1 << 20 // 1 MiB

// Idempotency returns middleware that caches responses for POST/PUT/PATCH
// requests that include an Idempotency-Key header. If a request with the same
// key has been processed before, the cached response is returned.
//
// Stripe-compatible enforcement:
//   - Same key + same (method, path, body) → replay cached response
//     (sets Idempotent-Replayed: true header).
//   - Same key + different (method, path, body) → 422 idempotency_error
//     (protects against client bugs that recycle a key across operations —
//     e.g., retrying POST /invoices with a changed amount under the old key).
func Idempotency(db *postgres.DB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "POST" && r.Method != "PUT" && r.Method != "PATCH" {
				next.ServeHTTP(w, r)
				return
			}

			key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
			if key == "" {
				next.ServeHTTP(w, r)
				return
			}

			tenantID := auth.TenantID(r.Context())
			if tenantID == "" {
				next.ServeHTTP(w, r)
				return
			}

			// Read the body so we can (a) hash it for the fingerprint and
			// (b) hand a fresh reader to the downstream handler.
			bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, maxIdempotentBodyBytes+1))
			if err != nil {
				ErrorJSON(w, http.StatusBadRequest, "bad_request", "failed to read body")
				return
			}
			_ = r.Body.Close()
			if len(bodyBytes) > maxIdempotentBodyBytes {
				// Body exceeds our hash cap — hash only the method+path+prefix
				// rather than refuse, so large requests aren't broken. The
				// fingerprint just becomes weaker (bodies matching on prefix
				// would false-match), which is acceptable given the rarity.
				bodyBytes = bodyBytes[:maxIdempotentBodyBytes]
			}
			r.Body = io.NopCloser(bytes.NewReader(bodyBytes))

			fingerprint := fingerprintRequest(r.Method, r.URL.Path, bodyBytes)

			cached, found, err := getCachedResponse(r.Context(), db, tenantID, key)
			if err != nil {
				// DB error — fail open (don't block the write). An idempotency
				// check failure shouldn't prevent the actual operation.
				next.ServeHTTP(w, r)
				return
			}
			if found {
				// Fingerprint mismatch → key reused with different parameters.
				// Stripe returns 422 idempotency_error here.
				if len(cached.Fingerprint) > 0 &&
					subtle.ConstantTimeCompare(cached.Fingerprint, fingerprint) != 1 {
					ErrorJSON(w, http.StatusUnprocessableEntity, "idempotency_error",
						"Keys for idempotent requests can only be used with the same parameters they were first used with.")
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Idempotent-Replayed", "true")
				w.WriteHeader(cached.StatusCode)
				_, _ = w.Write(cached.Body)
				return
			}

			recorder := &responseRecorder{
				ResponseWriter: w,
				statusCode:     http.StatusOK,
				body:           &bytes.Buffer{},
			}
			next.ServeHTTP(recorder, r)

			// Cache 2xx/3xx/4xx/5xx except 409 and 422. The core value of
			// idempotency is that a transient 500 retry must not re-run side
			// effects — the first call may have already committed writes the
			// client can't observe (e.g., Stripe charge succeeded but our
			// response timed out). Without caching the 500, a retry under the
			// same key would double-charge.
			//
			// 409 Conflict and 422 Unprocessable Entity are excluded because
			// they signal "this isn't the real first response": 409 from
			// concurrent contention on shared state (a retry after the
			// contention clears may legitimately succeed), 422 typically
			// from input validation (the client usually fixes the body, and
			// our fingerprint check will flag that as a key-reuse error).
			// Pinning either would trap the caller.
			if recorder.statusCode != http.StatusConflict &&
				recorder.statusCode != http.StatusUnprocessableEntity {
				storeCachedResponse(context.Background(), db, tenantID, key,
					r.Method, r.URL.Path, fingerprint,
					recorder.statusCode, recorder.body.Bytes())
			}
		})
	}
}

// fingerprintRequest returns a stable hash of the request's identifying
// parameters. Method+path keeps a key bound to a single endpoint; body keeps
// the same endpoint bound to the same payload.
func fingerprintRequest(method, path string, body []byte) []byte {
	h := sha256.New()
	h.Write([]byte(method))
	h.Write([]byte{0})
	h.Write([]byte(path))
	h.Write([]byte{0})
	h.Write(body)
	return h.Sum(nil)
}

type cachedResponse struct {
	StatusCode  int
	Body        []byte
	Fingerprint []byte
}

func getCachedResponse(ctx context.Context, db *postgres.DB, tenantID, key string) (cachedResponse, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	tx, err := db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return cachedResponse{}, false, err
	}
	defer postgres.Rollback(tx)

	var c cachedResponse
	err = tx.QueryRowContext(ctx,
		`SELECT status_code, response_body, request_fingerprint FROM idempotency_keys
		WHERE tenant_id = $1 AND key = $2 AND expires_at > now()`,
		tenantID, key,
	).Scan(&c.StatusCode, &c.Body, &c.Fingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return c, false, nil
	}
	if err != nil {
		return c, false, err
	}
	if err := tx.Commit(); err != nil {
		return c, false, err
	}
	return c, true, nil
}

func storeCachedResponse(ctx context.Context, db *postgres.DB, tenantID, key, method, path string, fingerprint []byte, statusCode int, body []byte) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	tx, err := db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return
	}
	defer postgres.Rollback(tx)

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO idempotency_keys (key, tenant_id, http_method, http_path, request_fingerprint, status_code, response_body)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (tenant_id, key) DO NOTHING`,
		key, tenantID, method, path, fingerprint, statusCode, body,
	); err != nil {
		return
	}
	_ = tx.Commit()
}

// responseRecorder captures the response for caching while writing to the client.
type responseRecorder struct {
	http.ResponseWriter
	statusCode int
	body       *bytes.Buffer
}

func (r *responseRecorder) WriteHeader(code int) {
	r.statusCode = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	r.body.Write(b)
	return r.ResponseWriter.Write(b)
}

// CleanExpired removes expired idempotency keys across all tenants and
// returns the number of rows deleted. Runs cross-tenant by design (a
// background scheduler, not a per-request path), so it uses TxBypass to
// sidestep the tenant_isolation policy.
func CleanExpired(ctx context.Context, db *postgres.DB) (int, error) {
	tx, err := db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return 0, err
	}
	defer postgres.Rollback(tx)

	res, err := tx.ExecContext(ctx, `DELETE FROM idempotency_keys WHERE expires_at < now()`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int(n), nil
}

// IdempotencyCleaner adapts CleanExpired to the scheduler's cleaner
// interface (Cleanup(ctx) (int, error)), matching the shape used by
// payment.TokenService. Keeps scheduler wiring identical across cleanup
// tasks, and avoids the scheduler package importing anything from middleware.
type IdempotencyCleaner struct {
	db *postgres.DB
}

func NewIdempotencyCleaner(db *postgres.DB) *IdempotencyCleaner {
	return &IdempotencyCleaner{db: db}
}

func (c *IdempotencyCleaner) Cleanup(ctx context.Context) (int, error) {
	return CleanExpired(ctx, c.db)
}

// ErrorJSON is a helper that returns a Stripe-style error response for
// idempotency key misuse.
func ErrorJSON(w http.ResponseWriter, status int, errType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{
			"type":    errType,
			"message": message,
		},
	})
}
