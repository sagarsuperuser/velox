package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"net/http"
	"strings"
	"time"

	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/sagarsuperuser/velox/internal/audit"
	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

// auditWriter persists a single audit entry. Introduced as an injection
// point so the middleware logic (buffering, failure branch) can be unit
// tested without a live database. Production uses the postgres-backed
// implementation below.
type auditWriter interface {
	Write(ctx context.Context, tenantID, action, resourceType, resourceID, resourceLabel, path string) error
}

type postgresAuditWriter struct{ db *postgres.DB }

func (p *postgresAuditWriter) Write(ctx context.Context, tenantID, action, resourceType, resourceID, resourceLabel, path string) error {
	return writeAudit(ctx, p.db, tenantID, action, resourceType, resourceID, resourceLabel, path)
}

// bufferedResponse captures status, headers, and body so the middleware can
// read the response JSON (extractLabel / extractID) before flushing it.
// Handlers see a normal ResponseWriter and have no awareness the response
// is held back. The buffer is flushed VERBATIM on every path — ADR-089 bans
// replacing a committed mutation's response (the old fail-closed 503 swap
// was cached by the Idempotency layer and poisoned keys for 24h).
type bufferedResponse struct {
	header      http.Header
	status      int
	body        bytes.Buffer
	wroteHeader bool
}

func newBufferedResponse() *bufferedResponse {
	return &bufferedResponse{header: make(http.Header), status: http.StatusOK}
}

func (b *bufferedResponse) Header() http.Header { return b.header }

func (b *bufferedResponse) WriteHeader(status int) {
	if b.wroteHeader {
		return
	}
	b.status = status
	b.wroteHeader = true
}

func (b *bufferedResponse) Write(p []byte) (int, error) {
	if !b.wroteHeader {
		b.WriteHeader(http.StatusOK)
	}
	return b.body.Write(p)
}

func (b *bufferedResponse) flushTo(w http.ResponseWriter) {
	maps.Copy(w.Header(), b.header)
	w.WriteHeader(b.status)
	_, _ = w.Write(b.body.Bytes())
}

// AuditLog returns middleware that records every successful mutating request
// (POST/PUT/PATCH/DELETE outside system endpoints) to audit_log. The write
// is synchronous because the entry is compliance evidence, not best-effort.
//
// On audit write failure the request is served anyway (fail-open): log +
// increment velox_audit_write_errors_total; flush the handler response
// untouched. The former per-tenant fail-closed mode (503 audit_error
// replacing the committed response) is retired — see ADR-089: the business
// tx had already committed, so the 503 was a lie the Idempotency layer then
// cached for 24h, stranding the real response and inviting fresh-key
// double-mutations. Fail-closed semantics return, structurally, with in-tx
// audit emission (the audit redesign's LogInTx), where mutation and audit
// row share one transaction and there is no post-commit window to police.
func AuditLog(db *postgres.DB) func(http.Handler) http.Handler {
	return auditLogWith(&postgresAuditWriter{db: db})
}

func auditLogWith(writer auditWriter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "POST" && r.Method != "PUT" && r.Method != "PATCH" && r.Method != "DELETE" {
				next.ServeHTTP(w, r)
				return
			}

			// System endpoints are not operator actions — skip audit entirely.
			if strings.HasPrefix(r.URL.Path, "/v1/bootstrap") ||
				strings.HasPrefix(r.URL.Path, "/v1/webhooks/stripe") ||
				strings.HasPrefix(r.URL.Path, "/health") ||
				strings.HasPrefix(r.URL.Path, "/metrics") {
				next.ServeHTTP(w, r)
				return
			}

			// Seed the request ctx with audit bookkeeping so handlers that make
			// explicit audit.Logger.Log calls can suppress our catch-all write
			// (MarkHandled), and so both write paths see the same client IP.
			ctx := audit.WithRequestState(r.Context())
			ctx = audit.WithClientIP(ctx, audit.ExtractClientIP(r))
			r = r.WithContext(ctx)

			buf := newBufferedResponse()
			next.ServeHTTP(buf, r)

			// Non-2xx: business mutation didn't succeed, nothing to audit.
			// Flush whatever the handler produced (error body, headers) and return.
			if buf.status < 200 || buf.status >= 300 {
				buf.flushTo(w)
				return
			}

			tenantID := auth.TenantID(r.Context())
			if tenantID == "" {
				buf.flushTo(w)
				return
			}

			// If a handler already wrote an explicit audit row for this
			// request, don't duplicate it here.
			if audit.WasHandled(r.Context()) {
				buf.flushTo(w)
				return
			}

			action, resourceType, resourceID := parseAuditPath(r.Method, r.URL.Path)
			resourceLabel := extractLabel(buf.body.Bytes())

			// Creates (POST /v1/{resource}) carry no id in the URL — the
			// new id lives in the response body. Without this fallback,
			// the audit row records resource_id="" and the dashboard's
			// "View" link becomes /customers/, /invoices/, etc.
			if action == "create" && resourceID == "" {
				resourceID = extractID(buf.body.Bytes())
			}

			if err := writer.Write(r.Context(), tenantID,
				action, resourceType, resourceID, resourceLabel, r.URL.Path); err != nil {
				RecordAuditWriteError(tenantID)
				slog.Error("audit write failed — request served anyway (fail-open)",
					"error", err, "tenant_id", tenantID,
					"action", action, "resource_type", resourceType, "resource_id", resourceID)
			}

			buf.flushTo(w)
		})
	}
}

// extractLabel pulls a human-readable label from the response JSON.
func extractLabel(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return ""
	}

	// Try common label fields in order of specificity
	for _, key := range []string{
		"invoice_number",     // invoices
		"credit_note_number", // credit notes
		"display_name",       // customers, subscriptions
		"name",               // plans, meters, rating rules, webhooks
		"key",                // meters
	} {
		if v, ok := m[key]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}

	// Nested: e.g. { "subscription": { "display_name": "..." } }
	if sub, ok := m["subscription"]; ok {
		if sm, ok := sub.(map[string]any); ok {
			if v, ok := sm["display_name"]; ok {
				if s, ok := v.(string); ok && s != "" {
					return s
				}
			}
		}
	}

	return ""
}

// extractID pulls the new resource id from a 2xx create response. Used by
// the middleware to fill in resource_id for POST /v1/{resource} where the
// URL carries no id. Top-level `id` covers bare-object responses (the
// common shape — `respond.JSON(w, r, 201, customer)`); the nested cases
// mirror extractLabel so wrapped responses (e.g. `{"subscription": {…}}`)
// still resolve.
func extractID(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return ""
	}
	if v, ok := m["id"]; ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	for _, key := range []string{"subscription", "invoice", "customer", "plan", "meter"} {
		if sub, ok := m[key]; ok {
			if sm, ok := sub.(map[string]any); ok {
				if v, ok := sm["id"]; ok {
					if s, ok := v.(string); ok && s != "" {
						return s
					}
				}
			}
		}
	}
	return ""
}

func parseAuditPath(method, path string) (action, resourceType, resourceID string) {
	// Remove /v1/ prefix
	path = strings.TrimPrefix(path, "/v1/")
	parts := strings.Split(path, "/")

	if len(parts) == 0 {
		return method, "unknown", ""
	}

	resourceType = parts[0]

	// Handle 2-part paths with known actions: billing/run, credits/grant, etc.
	if len(parts) == 2 {
		switch parts[1] {
		case "run":
			return "run", canonicalResourceType(parts[0]), ""
		case "grant":
			return "grant", canonicalResourceType(parts[0]), ""
		case "adjust":
			return "adjust", canonicalResourceType(parts[0]), ""
		}
	}

	// Map path actions to audit actions
	if len(parts) >= 3 {
		// e.g., /subscriptions/{id}/cancel → cancel, subscription, {id}
		lastPart := parts[len(parts)-1]
		switch lastPart {
		case "activate", "cancel", "pause", "resume", "finalize", "void", "issue", "resolve", "replay", "run", "grant", "adjust", "rotate", "archive", "unarchive":
			action = lastPart
			resourceID = parts[1]
			resourceType = canonicalResourceType(resourceType)
			return
		case "change-plan":
			action = "change_plan"
			resourceID = parts[1]
			resourceType = canonicalResourceType(resourceType)
			return
		case "billing-profile":
			action = "update"
			resourceType = "billing_profile"
			resourceID = parts[1]
			return
		}
	}

	// Default: derive from HTTP method
	switch method {
	case "POST":
		action = "create"
	case "PUT", "PATCH":
		action = "update"
	case "DELETE":
		action = "delete"
	}

	if len(parts) >= 2 {
		resourceID = parts[1]
	}

	resourceType = canonicalResourceType(resourceType)
	return
}

// canonicalResourceType normalizes the leading URL path segment to the
// singular/snake-case form used by handler-emitted audit rows (and by the
// UI's filter vocabulary). Must be applied in every parseAuditPath branch —
// the previous "return early without normalizing" path let action-style URLs
// (/cancel, /pause, …) record resource_type as "subscriptions" (plural),
// which the UI filter never matched.
func canonicalResourceType(s string) string {
	s = strings.TrimSuffix(s, "s")
	switch s {
	case "rating-rule":
		return "rating_rule"
	case "usage-event":
		return "usage_event"
	case "credit-note":
		return "credit_note"
	case "api-key":
		return "api_key"
	case "webhook-endpoint":
		return "webhook_endpoint"
	}
	return s
}

// writeAudit persists an audit entry synchronously. Returns nil on success
// or the first DB error — the caller decides whether to fail the request.
func writeAudit(parentCtx context.Context, db *postgres.DB, tenantID, action, resourceType, resourceID, resourceLabel, path string) error {
	// Use a detached timeout context so a client disconnect on the parent request
	// does not abort the audit write mid-flight. 3s is generous for a single INSERT.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parentCtx), 3*time.Second)
	defer cancel()

	tx, err := db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return err
	}
	defer postgres.Rollback(tx)

	id := postgres.NewID("vlx_aud")
	metaJSON, _ := json.Marshal(map[string]string{"path": path})

	// Resolve actor from the original request ctx (session user / api key /
	// customer / system) — same shared helper Logger.Log uses, so a dashboard
	// operator on a session cookie records actor_type='user', not 'system'.
	actorType, actorID := audit.ResolveActor(parentCtx)

	ipAddress := audit.ClientIP(parentCtx)
	requestID := chimw.GetReqID(parentCtx)

	// created_at is wall-clock — the audit row records when the operator
	// hit the endpoint, not the simulated effect-time on the affected
	// entity. ADR-030 line 131. Handlers that want the sim-effective-at
	// of an action recorded should call audit.Logger.Log directly with
	// {sim_effective_at, test_clock_id} in the metadata bag; the
	// middleware catch-all only fires when no handler wrote an audit row.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO audit_log (id, tenant_id, actor_type, actor_id, action,
			resource_type, resource_id, resource_label, metadata, ip_address,
			request_id, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
	`, id, tenantID, actorType, actorID, action, resourceType, resourceID, resourceLabel, metaJSON,
		nullIfEmpty(ipAddress), nullIfEmpty(requestID), time.Now().UTC()); err != nil {
		return err
	}

	return tx.Commit()
}

// nullIfEmpty keeps optional text columns as SQL NULL rather than "" — lets
// COUNT(ip_address IS NOT NULL) be meaningful and avoids treating a blank
// header value as a recorded IP.
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
