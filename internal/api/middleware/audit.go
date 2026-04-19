package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

// responseCapture wraps ResponseWriter to capture the response body.
type responseCapture struct {
	http.ResponseWriter
	body *bytes.Buffer
}

func (rc *responseCapture) Write(b []byte) (int, error) {
	rc.body.Write(b)
	return rc.ResponseWriter.Write(b)
}

// AuditLog returns middleware that automatically logs all mutating requests
// (POST, PUT, PATCH, DELETE) to the audit_log table.
func AuditLog(db *postgres.DB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Only audit mutating methods
			if r.Method != "POST" && r.Method != "PUT" && r.Method != "PATCH" && r.Method != "DELETE" {
				next.ServeHTTP(w, r)
				return
			}

			// Skip system endpoints (not operator actions)
			if strings.HasPrefix(r.URL.Path, "/v1/bootstrap") ||
				strings.HasPrefix(r.URL.Path, "/v1/webhooks/stripe") ||
				strings.HasPrefix(r.URL.Path, "/health") ||
				strings.HasPrefix(r.URL.Path, "/metrics") {
				next.ServeHTTP(w, r)
				return
			}

			// Capture response
			ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
			capture := &responseCapture{ResponseWriter: ww, body: &bytes.Buffer{}}
			next.ServeHTTP(capture, r)

			// Only log successful mutations (2xx)
			if ww.Status() < 200 || ww.Status() >= 300 {
				return
			}

			tenantID := auth.TenantID(r.Context())
			if tenantID == "" {
				return
			}

			action, resourceType, resourceID := parseAuditPath(r.Method, r.URL.Path)
			resourceLabel := extractLabel(capture.body.Bytes(), resourceType)

			// Synchronous: audit is compliance evidence, not best-effort. Fire-and-forget
			// goroutines can die with the request context or lose errors silently, leaving
			// gaps in the audit trail. We accept the added ~5-20ms DB insert latency in
			// exchange for guaranteed writes. Failures are logged but do not fail the
			// request — the business mutation has already succeeded by this point.
			writeAudit(r.Context(), db, tenantID, auth.KeyID(r.Context()), action, resourceType, resourceID, resourceLabel, r.URL.Path)
		})
	}
}

// extractLabel pulls a human-readable label from the response JSON.
func extractLabel(body []byte, resourceType string) string {
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
		case "activate", "cancel", "pause", "resume", "finalize", "void", "issue", "resolve", "replay", "run", "grant", "adjust":
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

func writeAudit(parentCtx context.Context, db *postgres.DB, tenantID, actorID, action, resourceType, resourceID, resourceLabel, path string) {
	// Use a detached timeout context so a client disconnect on the parent request
	// does not abort the audit write mid-flight. 3s is generous for a single INSERT.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parentCtx), 3*time.Second)
	defer cancel()

	tx, err := db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		slog.Error("audit write: begin tx failed",
			"error", err, "tenant_id", tenantID, "action", action,
			"resource_type", resourceType, "resource_id", resourceID)
		return
	}
	defer postgres.Rollback(tx)

	id := postgres.NewID("vlx_aud")
	metaJSON, _ := json.Marshal(map[string]string{"path": path})

	actorType := "api_key"
	if actorID == "" {
		actorType = "system"
		actorID = "system"
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO audit_log (id, tenant_id, actor_type, actor_id, action,
			resource_type, resource_id, resource_label, metadata, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
	`, id, tenantID, actorType, actorID, action, resourceType, resourceID, resourceLabel, metaJSON, time.Now().UTC())

	if err != nil {
		slog.Error("audit write: insert failed",
			"error", err, "tenant_id", tenantID, "action", action,
			"resource_type", resourceType, "resource_id", resourceID)
		return
	}
	if err := tx.Commit(); err != nil {
		slog.Error("audit write: commit failed",
			"error", err, "tenant_id", tenantID, "action", action,
			"resource_type", resourceType, "resource_id", resourceID)
	}
}
