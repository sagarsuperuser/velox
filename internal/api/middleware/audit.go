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

	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

// AuditSettingsLookup returns whether a tenant has opted into fail-closed
// audit logging. Kept as a narrow interface so the middleware can be tested
// without a real SettingsStore.
type AuditSettingsLookup interface {
	IsAuditFailClosed(ctx context.Context, tenantID string) (bool, error)
}

// auditWriter persists a single audit entry. Introduced as an injection
// point so the middleware logic (buffering, fail-closed branch) can be unit
// tested without a live database. Production uses the postgres-backed
// implementation below.
type auditWriter interface {
	Write(ctx context.Context, tenantID, actorID, action, resourceType, resourceID, resourceLabel, path string) error
}

type postgresAuditWriter struct{ db *postgres.DB }

func (p *postgresAuditWriter) Write(ctx context.Context, tenantID, actorID, action, resourceType, resourceID, resourceLabel, path string) error {
	return writeAudit(ctx, p.db, tenantID, actorID, action, resourceType, resourceID, resourceLabel, path)
}

// bufferedResponse captures status, headers, and body so the middleware can
// decide whether to flush the handler's response or replace it with 503
// after the audit write attempt. Handlers see a normal ResponseWriter and
// have no awareness the response is held back.
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
// On audit write failure the tenant's audit_fail_closed setting decides:
//   - fail-open (default): log + increment metric; flush the handler response.
//     Preserves availability at the cost of an accepted compliance gap that
//     operators must notice via the metric.
//   - fail-closed: log + increment metric; return 503 audit_error instead of
//     the handler's response. Paired with API idempotency keys so client
//     retries don't double-mutate when the business tx already committed.
func AuditLog(db *postgres.DB, settings AuditSettingsLookup) func(http.Handler) http.Handler {
	return auditLogWith(&postgresAuditWriter{db: db}, settings)
}

func auditLogWith(writer auditWriter, settings AuditSettingsLookup) func(http.Handler) http.Handler {
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

			action, resourceType, resourceID := parseAuditPath(r.Method, r.URL.Path)
			resourceLabel := extractLabel(buf.body.Bytes())

			if err := writer.Write(r.Context(), tenantID, auth.KeyID(r.Context()),
				action, resourceType, resourceID, resourceLabel, r.URL.Path); err != nil {
				RecordAuditWriteError(tenantID)

				failClosed := false
				if settings != nil {
					fc, lookupErr := settings.IsAuditFailClosed(r.Context(), tenantID)
					if lookupErr != nil {
						// Can't determine policy — fail-safe to closed so a
						// broken settings lookup doesn't silently downgrade a
						// SOC-2 tenant to fail-open.
						slog.Error("audit fail-closed lookup failed",
							"error", lookupErr, "tenant_id", tenantID)
						failClosed = true
					} else {
						failClosed = fc
					}
				}

				if failClosed {
					slog.Error("audit write failed — returning 503 (fail-closed)",
						"error", err, "tenant_id", tenantID,
						"action", action, "resource_type", resourceType, "resource_id", resourceID)
					writeAuditError(w)
					return
				}

				slog.Error("audit write failed — request served anyway (fail-open)",
					"error", err, "tenant_id", tenantID,
					"action", action, "resource_type", resourceType, "resource_id", resourceID)
			}

			buf.flushTo(w)
		})
	}
}

// writeAuditError emits the 503 body returned to fail-closed tenants when
// the audit write fails. Matches the shape of respond.JSON error envelopes
// so clients can treat it like any other API error.
func writeAuditError(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte(`{"error":{"code":"audit_error","message":"audit log unavailable; request not completed from an auditing standpoint — retry"}}`))
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
			return "run", parts[0], "" // "run billing"
		case "grant":
			return "grant", parts[0], ""
		case "adjust":
			return "adjust", parts[0], ""
		}
	}

	// Map path actions to audit actions
	if len(parts) >= 3 {
		// e.g., /subscriptions/{id}/cancel → cancel, subscription, {id}
		lastPart := parts[len(parts)-1]
		switch lastPart {
		case "activate", "cancel", "pause", "resume", "finalize", "void", "issue", "resolve", "replay", "run", "grant", "adjust", "rotate":
			action = lastPart
			resourceID = parts[1]
			return
		case "change-plan":
			action = "change_plan"
			resourceID = parts[1]
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

	// Singularize resource type
	resourceType = strings.TrimSuffix(resourceType, "s")
	if resourceType == "rating-rule" {
		resourceType = "rating_rule"
	}
	if resourceType == "usage-event" {
		resourceType = "usage_event"
	}
	if resourceType == "credit-note" {
		resourceType = "credit_note"
	}
	if resourceType == "api-key" {
		resourceType = "api_key"
	}
	if resourceType == "webhook-endpoint" {
		resourceType = "webhook_endpoint"
	}

	return
}

// writeAudit persists an audit entry synchronously. Returns nil on success
// or the first DB error — the caller decides whether to fail the request.
func writeAudit(parentCtx context.Context, db *postgres.DB, tenantID, actorID, action, resourceType, resourceID, resourceLabel, path string) error {
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

	actorType := "api_key"
	if actorID == "" {
		actorType = "system"
		actorID = "system"
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO audit_log (id, tenant_id, actor_type, actor_id, action,
			resource_type, resource_id, resource_label, metadata, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
	`, id, tenantID, actorType, actorID, action, resourceType, resourceID, resourceLabel, metaJSON, time.Now().UTC()); err != nil {
		return err
	}

	return tx.Commit()
}
