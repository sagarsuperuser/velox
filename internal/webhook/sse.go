package webhook

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/domain"
)

// streamEvents serves the live-tail SSE feed for a tenant's webhook
// events. The handler:
//
//  1. Sends a snapshot of recent events (ListEvents, last 50) so a
//     freshly-opened dashboard renders the current state immediately,
//     not a blank table.
//  2. Subscribes to the EventBus so any new Dispatch / Replay /
//     deliver-result publishes a frame to this connection.
//  3. Sends a heartbeat comment every 15s — proxies (nginx, ALB) idle
//     connections at 60s by default; a periodic ":\n\n" line keeps the
//     pipe warm without polluting the data channel.
//
// Tenant scoping is enforced two ways: ListEvents runs under the
// caller's tenant tx (RLS), and the EventBus subscriber is keyed by
// tenant_id so cross-tenant frames never reach this connection.
//
// We do NOT poll the DB on a tick — the EventBus is in-memory and
// source-of-truth-adjacent (the Service publishes synchronously from
// Dispatch). That means a single replica can serve the stream; the
// outbox dispatcher's success path doesn't currently publish frames
// (it ends up calling Service.Dispatch which does), so multi-replica
// SSE works as long as the tenant's HTTP traffic and dispatcher land
// on the same replica. v1 sizing is single-replica per CLAUDE.md, so
// this is correct for now; a v2 multi-replica deployment would route
// SSE through Postgres LISTEN/NOTIFY (one-line swap inside EventBus).
const (
	sseHeartbeatInterval = 15 * time.Second
	sseSnapshotLimit     = 50
)

// streamEvents is the chi handler. Mounted at GET /webhook_events/stream
// — registered BEFORE /webhook_events/{id} so chi's specificity-by-
// registration-order rule picks the literal route.
func (h *Handler) streamEvents(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	if tenantID == "" {
		http.Error(w, "tenant required", http.StatusUnauthorized)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// SSE headers. Cache-Control:no-cache prevents proxies from
	// buffering or replaying stale chunks; X-Accel-Buffering disables
	// nginx's response buffering specifically.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// Snapshot first: the dashboard renders these so a freshly-opened
	// page shows recent activity instead of a blank table while
	// waiting for the next live event. Newest-first is the SSE
	// convention; the frontend prepends new live frames on top so the
	// ordering is consistent.
	snapshot, err := h.svc.ListEvents(r.Context(), tenantID, sseSnapshotLimit)
	if err != nil {
		slog.ErrorContext(r.Context(), "sse snapshot failed", "error", err)
	}
	for _, e := range snapshot {
		writeFrame(w, FrameFromEvent(e, frameStatusFromEvent(e), nil))
	}
	flusher.Flush()

	// Subscribe to the live bus AFTER the snapshot completes so we
	// don't double-emit any event that arrived during snapshot
	// streaming. There's still a sub-millisecond window of "live event
	// queued + snapshot SELECT pulled it"; the dashboard de-dupes by
	// event_id so a stray double-frame just collapses into the same
	// row.
	frames, cancel := h.svc.EventBus().Subscribe(tenantID)
	defer cancel()

	heartbeat := time.NewTicker(sseHeartbeatInterval)
	defer heartbeat.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			// SSE comment lines (": ...\n\n") keep idle proxies from
			// dropping the connection. The data channel stays clean.
			if _, err := w.Write([]byte(": heartbeat\n\n")); err != nil {
				return
			}
			flusher.Flush()
		case frame, ok := <-frames:
			if !ok {
				return
			}
			writeFrame(w, frame)
			flusher.Flush()
		}
	}
}

// writeFrame serializes a single SSE event frame. We use the named
// `event: webhook_event` form so the EventSource consumer can attach
// addEventListener('webhook_event', ...) and ignore heartbeats /
// future event types. Errors writing to a closed connection are
// swallowed — the request context will fire next tick and the handler
// loop exits.
func writeFrame(w http.ResponseWriter, frame StreamFrame) {
	blob, err := json.Marshal(frame)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "event: webhook_event\ndata: %s\n\n", blob)
}

// frameStatusFromEvent infers the dashboard status for a snapshot
// event. Real-time frames carry status from the deliver result; the
// snapshot path doesn't have that handy without a JOIN, so we surface
// "delivered" for events that are old enough that any retries would
// have either succeeded or DLQ'd, and "pending" otherwise. The
// dashboard treats both as informational — clicking the row hits
// /deliveries to see the per-attempt truth.
func frameStatusFromEvent(e domain.WebhookEvent) string {
	// 24h+ old: any deliver retry would have completed by now (the
	// retry ramp tops out at ~24h cumulative — see retryBackoffs).
	if time.Since(e.CreatedAt) > 24*time.Hour {
		return "delivered"
	}
	return "pending"
}

// hashPayload computes the canonical SHA-256 of an event payload's
// JSON serialization. The deliveries-list endpoint surfaces this so
// the dashboard's diff view can flag "payload identical between
// attempts" (the common case — Stripe-style replays don't mutate the
// payload). Returns empty string on marshal failure rather than
// crashing the handler.
func hashPayload(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	blob, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(blob)
	return hex.EncodeToString(sum[:])
}

// truncateBody is the wire-shape contract guard: response bodies are
// capped at 4KB before hitting the dashboard so a misbehaving receiver
// returning a megabyte HTML page doesn't blow out the deliveries-list
// payload size.
func truncateBody(s string) string {
	const max = 4 * 1024
	if len(s) <= max {
		return s
	}
	return s[:max]
}

// _ ensures context import is used when the file is built standalone.
var _ = context.Background
