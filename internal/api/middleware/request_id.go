package middleware

import (
	"context"
	"net/http"

	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/rs/xid"
)

// requestIDPrefix marks an id as server-minted. Rows written before this
// middleware landed may carry a client-supplied request_id (append-only: they
// cannot be rewritten), so the prefix is also the marker that tells a forensic
// reader which era a row belongs to — a `req_` id is provably ours.
const requestIDPrefix = "req_"

// RequestID replaces chi's middleware.RequestID. It ALWAYS mints the id
// server-side and NEVER honours an inbound header.
//
// chi's version does this (chi/v5 middleware/request_id.go):
//
//	requestID := r.Header.Get("X-Request-Id")   // <- client-controlled
//	if requestID == "" { requestID = <generated> }
//
// which means any caller could choose the request_id that lands on their own
// audit_log rows — the column the audit UI presents as forensic correlation
// evidence, and the value support uses to join a customer's report back to
// server logs. An attacker could set it to a constant to make their actions
// unjoinable, collide it with an innocent tenant's traffic, or forge a value
// that "proves" an action came from somewhere it didn't. Correlation evidence
// an adversary can write is not evidence. CloudTrail's eventID, Stripe's
// request id, and GCP's insertId are all server-minted for exactly this reason.
//
// Why we drop the inbound value entirely instead of keeping it for log
// correlation under a second key:
//
//   - Nothing consumes it. Grep for X-Request-Id across the repo, web-v2 and
//     docs: there are no hits. Velox's published contract is the Velox-Request-Id
//     RESPONSE header (respond.go), which the dashboard captures (web-v2
//     lib/api.ts) and the docs point support at. That contract is unchanged.
//   - Cross-service trace continuity is already carried properly by W3C Trace
//     Context, which mw.Tracing() (otelhttp) propagates from inbound headers.
//     X-Request-Id would be a second, weaker, unauthenticated tracing channel.
//   - Recording the client's string anywhere on the row — even under an
//     honestly-named metadata key — puts unverified client input into a
//     permanent append-only compliance record. The audit redesign's rule is that
//     nothing unverified enters that log; a key nobody reads is not worth the
//     exception.
//
// The id is stored under chi's own RequestIDKey, so chimw.GetReqID(ctx) — the
// accessor audit.Logger, telemetry.ContextHandler, respond.go and payment/stripe
// all already call — keeps working unchanged, and there is exactly one place
// where a request id is born.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), chimw.RequestIDKey, requestIDPrefix+xid.New().String())
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
