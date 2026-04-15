package middleware

import (
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// Tracing returns middleware that creates OpenTelemetry spans for each HTTP request.
// Propagates trace context from incoming headers (W3C Trace Context).
// When tracing is disabled (noop provider), this adds negligible overhead.
func Tracing() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return otelhttp.NewHandler(next, "velox-http",
			otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
				return r.Method + " " + sanitizePath(r.URL.Path)
			}),
		)
	}
}
