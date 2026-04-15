package middleware

import (
	"net/http"
	"os"
	"strings"
)

// SecurityHeaders adds standard security headers to all responses.
// These are defense-in-depth measures that enterprise security audits expect.
func SecurityHeaders() func(http.Handler) http.Handler {
	env := strings.ToLower(strings.TrimSpace(os.Getenv("APP_ENV")))

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// HSTS: tell browsers to always use HTTPS (skip in local dev)
			if env != "local" {
				w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
			}

			// Prevent MIME sniffing
			w.Header().Set("X-Content-Type-Options", "nosniff")

			// Prevent clickjacking
			w.Header().Set("X-Frame-Options", "DENY")

			// Disable client-side caching for API responses (financial data)
			w.Header().Set("Cache-Control", "no-store")

			// Referrer policy — don't leak URLs to third parties
			w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")

			next.ServeHTTP(w, r)
		})
	}
}

// MetricsAuth protects the /metrics endpoint with a bearer token.
// If METRICS_TOKEN is not set, /metrics is open (backward compatible for dev).
// In production, set METRICS_TOKEN and configure Prometheus to send it.
func MetricsAuth(next http.Handler) http.Handler {
	token := strings.TrimSpace(os.Getenv("METRICS_TOKEN"))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// No token configured — allow access (dev mode)
		if token == "" {
			next.ServeHTTP(w, r)
			return
		}

		// Check Authorization: Bearer <token>
		auth := r.Header.Get("Authorization")
		if auth == "" || !strings.HasPrefix(auth, "Bearer ") || strings.TrimPrefix(auth, "Bearer ") != token {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}
