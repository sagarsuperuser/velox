package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

// Idempotency returns middleware that caches responses for POST/PUT/PATCH
// requests that include an Idempotency-Key header. If a request with the same
// key has been processed before, the cached response is returned.
//
// This prevents double-charges and duplicate resource creation on retries.
func Idempotency(db *postgres.DB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Only apply to write methods
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

			// Check for cached response
			cached, err := getCachedResponse(r.Context(), db, tenantID, key)
			if err == nil {
				// Cache hit — return cached response
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Idempotency-Replayed", "true")
				w.WriteHeader(cached.StatusCode)
				w.Write(cached.Body)
				return
			}

			// Cache miss — execute the handler and capture the response
			recorder := &responseRecorder{
				ResponseWriter: w,
				statusCode:     http.StatusOK,
				body:           &bytes.Buffer{},
			}

			next.ServeHTTP(recorder, r)

			// Only cache successful responses (2xx)
			if recorder.statusCode >= 200 && recorder.statusCode < 300 {
				storeCachedResponse(context.Background(), db, tenantID, key, r.Method, r.URL.Path,
					recorder.statusCode, recorder.body.Bytes())
			}
		})
	}
}

type cachedResponse struct {
	StatusCode int
	Body       []byte
}

func getCachedResponse(ctx context.Context, db *postgres.DB, tenantID, key string) (cachedResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	var c cachedResponse
	err := db.Pool.QueryRowContext(ctx,
		`SELECT status_code, response_body FROM idempotency_keys
		WHERE tenant_id = $1 AND key = $2 AND expires_at > now()`,
		tenantID, key,
	).Scan(&c.StatusCode, &c.Body)
	return c, err
}

func storeCachedResponse(ctx context.Context, db *postgres.DB, tenantID, key, method, path string, statusCode int, body []byte) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	db.Pool.ExecContext(ctx,
		`INSERT INTO idempotency_keys (key, tenant_id, http_method, http_path, status_code, response_body)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (tenant_id, key) DO NOTHING`,
		key, tenantID, method, path, statusCode, body,
	)
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

// CleanExpired removes expired idempotency keys. Call periodically.
func CleanExpired(ctx context.Context, db *postgres.DB) error {
	_, err := db.Pool.ExecContext(ctx, `DELETE FROM idempotency_keys WHERE expires_at < now()`)
	return err
}

// ErrorJSON is a helper that returns a Stripe-style error response for
// idempotency key misuse.
func ErrorJSON(w http.ResponseWriter, status int, errType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{
			"type":    errType,
			"message": message,
		},
	})
}
