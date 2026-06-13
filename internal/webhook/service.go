package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	mrand "math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	mw "github.com/sagarsuperuser/velox/internal/api/middleware"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
	"github.com/sagarsuperuser/velox/internal/platform/scheduler"
)

const maxAttempts = 5

// retryLeaseWindow is how far ListPendingDeliveries pushes next_retry_at
// forward when it claims a due delivery, so a concurrent retry worker on
// another replica won't re-claim the same row while it's being delivered. Must
// comfortably exceed the per-attempt HTTP timeout (10s); if a claiming worker
// crashes mid-delivery, the lease expires after this window and the row becomes
// eligible again. Far below the smallest real retry backoff (1m) so it never
// delays a legitimate scheduled retry.
const retryLeaseWindow = 1 * time.Minute

// retryBackoffs defines the delay before each retry attempt (index 0 = after attempt 1).
var retryBackoffs = [maxAttempts]time.Duration{
	1 * time.Minute,
	5 * time.Minute,
	30 * time.Minute,
	2 * time.Hour,
	24 * time.Hour,
}

type Service struct {
	store       Store
	client      HTTPClient
	bus         *EventBus
	syncDeliver bool // When true, deliver synchronously (for tests)
}

// HTTPClient is the interface for making HTTP requests (mockable in tests).
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

func NewService(store Store, client HTTPClient) *Service {
	if client == nil {
		client = &http.Client{
			Timeout:   10 * time.Second,
			Transport: ssrfHardenedTransport(),
		}
	}
	return &Service{store: store, client: client, bus: NewEventBus()}
}

// ssrfHardenedTransport returns an http.Transport whose DialContext rejects
// connections to private/link-local/loopback/metadata IPs at the actual dial
// address. validateWebhookURL only resolves at CreateEndpoint time, so a host
// that resolved publicly then could be re-pointed at an internal address
// (DNS rebinding) by the time delivery POSTs to the stored URL. Checking the
// resolved dial target on every connection closes that window.
func ssrfHardenedTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = ssrfSafeDialContext
	return t
}

// ssrfSafeDialContext is the dial predicate wired into the delivery client.
// It resolves the host portion of addr and refuses to dial if the target
// address is a blocked (private/internal) IP.
func ssrfSafeDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if err := assertDialAddrAllowed(host); err != nil {
		return nil, err
	}
	d := &net.Dialer{Timeout: 10 * time.Second}
	return d.DialContext(ctx, network, net.JoinHostPort(host, port))
}

// assertDialAddrAllowed resolves host (an IP literal or hostname) and returns
// an error if any resolved address is a blocked private/internal IP. A
// hostname that resolves to multiple addresses is rejected if ANY of them is
// blocked — a partially-poisoned DNS answer must not slip an internal hop
// through. Reuses the same isBlockedIP predicate that the CreateEndpoint
// validation path relies on.
func assertDialAddrAllowed(host string) error {
	if ip := net.ParseIP(host); ip != nil {
		if isBlockedIP(ip) {
			return fmt.Errorf("refusing to dial private/internal address %s", ip)
		}
		return nil
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("resolve %q: %w", host, err)
	}
	for _, ip := range ips {
		if isBlockedIP(ip) {
			return fmt.Errorf("refusing to dial private/internal address %s (resolved from %q)", ip, host)
		}
	}
	return nil
}

// isBlockedIP returns true if ip is private, link-local, loopback, or
// otherwise reserved — covering both the IPv4 privateRanges table and the
// IPv6 equivalents (::1, fe80::/10, fc00::/7) that the IPv4-only table misses.
func isBlockedIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsPrivate() || ip.IsUnspecified() {
		return true
	}
	return isPrivateIP(ip)
}

// EventBus exposes the in-memory pub/sub backing the SSE stream. The
// handler subscribes per-request; the service publishes frames from
// Dispatch and the deliver path. Returns the same instance for the
// lifetime of the service so multiple handlers / cmd-side workers
// share one fan-out.
func (s *Service) EventBus() *EventBus { return s.bus }

// publishFrameForEventID looks up the event row and emits a status-
// transition frame to the SSE bus. Used from the failure paths where
// the caller has only the delivery (not the full event) in hand.
// Best-effort: a missed publish doesn't block delivery semantics.
func (s *Service) publishFrameForEventID(ctx context.Context, tenantID, eventID, status string, lastAttemptAt *time.Time) {
	if s.bus == nil || eventID == "" {
		return
	}
	ev, err := s.store.GetEvent(ctx, tenantID, eventID)
	if err != nil {
		return
	}
	s.bus.Publish(tenantID, FrameFromEvent(ev, status, lastAttemptAt))
}

// NewTestService creates a service with synchronous delivery (no goroutines).
func NewTestService(store Store, client HTTPClient) *Service {
	svc := NewService(store, client)
	svc.syncDeliver = true
	return svc
}

// privateRanges defines CIDR blocks that webhook URLs must not resolve to.
var privateRanges = []net.IPNet{
	parseCIDR("10.0.0.0/8"),     // RFC 1918
	parseCIDR("172.16.0.0/12"),  // RFC 1918
	parseCIDR("192.168.0.0/16"), // RFC 1918
	parseCIDR("127.0.0.0/8"),    // Loopback
	parseCIDR("169.254.0.0/16"), // Link-local
	parseCIDR("0.0.0.0/8"),      // "This" network
}

func parseCIDR(s string) net.IPNet {
	_, ipNet, err := net.ParseCIDR(s)
	if err != nil {
		panic("invalid CIDR: " + s)
	}
	return *ipNet
}

// isPrivateIP returns true if the IP falls within a blocked private/reserved range.
func isPrivateIP(ip net.IP) bool {
	for _, r := range privateRanges {
		if r.Contains(ip) {
			return true
		}
	}
	return false
}

// validateWebhookURL checks that a webhook URL uses HTTPS (or http for localhost)
// and does not resolve to a private/internal IP address (SSRF protection).
func validateWebhookURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return errs.Invalid("url", "must be a valid URL")
	}
	if parsed.Scheme != "https" && (parsed.Scheme != "http" || !strings.HasPrefix(parsed.Host, "localhost")) {
		return errs.Invalid("url", "must use HTTPS (except localhost)")
	}

	// Skip SSRF check for localhost (local development).
	host := parsed.Hostname()
	if host == "localhost" {
		return nil
	}

	// Resolve hostname and check all IPs against blocked ranges.
	ips, err := net.LookupHost(host)
	if err != nil {
		return errs.Invalid("url", fmt.Sprintf("cannot resolve host %q", host))
	}
	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			continue
		}
		if isPrivateIP(ip) {
			return errs.Invalid("url", fmt.Sprintf("must not resolve to a private/internal IP address (got %s)", ipStr))
		}
	}

	return nil
}

type CreateEndpointInput struct {
	URL         string   `json:"url"`
	Description string   `json:"description,omitempty"`
	Events      []string `json:"events"`
}

type CreateEndpointResult struct {
	Endpoint domain.WebhookEndpoint `json:"endpoint"`
	Secret   string                 `json:"secret"` // Shown once
}

func (s *Service) CreateEndpoint(ctx context.Context, tenantID string, input CreateEndpointInput) (CreateEndpointResult, error) {
	rawURL := strings.TrimSpace(input.URL)
	if rawURL == "" {
		return CreateEndpointResult{}, errs.Required("url")
	}
	if err := validateWebhookURL(rawURL); err != nil {
		return CreateEndpointResult{}, err
	}

	events := input.Events
	if len(events) == 0 {
		events = []string{"*"}
	}

	// Generate signing secret. A short read from the entropy pool would yield a
	// low-entropy or partially-zeroed HMAC key that still validates downstream —
	// treat any rand.Read error as fatal to endpoint creation rather than
	// minting a compromised secret.
	secretBytes := make([]byte, 24)
	if _, err := rand.Read(secretBytes); err != nil {
		return CreateEndpointResult{}, fmt.Errorf("generate webhook secret: %w", err)
	}
	secret := "whsec_" + hex.EncodeToString(secretBytes)

	ep, err := s.store.CreateEndpoint(ctx, tenantID, domain.WebhookEndpoint{
		URL:         rawURL,
		Description: strings.TrimSpace(input.Description),
		Secret:      secret,
		Events:      events,
		Active:      true,
	})
	if err != nil {
		return CreateEndpointResult{}, err
	}

	return CreateEndpointResult{Endpoint: ep, Secret: secret}, nil
}

// CreateEndpointTx forwards to the store's tx-aware insert. Used by
// recipe.Service.Instantiate so a recipe with a default outbound endpoint
// commits atomically with the rest of the recipe.
func (s *Service) CreateEndpointTx(ctx context.Context, tx *sql.Tx, tenantID string, ep domain.WebhookEndpoint) (domain.WebhookEndpoint, error) {
	return s.store.CreateEndpointTx(ctx, tx, tenantID, ep)
}

// SecretRotationGracePeriod is how long a rotated-out secret keeps
// signing alongside its replacement. Velox uses 72h; Stripe's hosted
// equivalent caps at 24h
// (https://docs.stripe.com/webhooks/signature). The intentional
// deviation: self-hosted Velox deployments often have slower deploy
// cadences (manual rolls, no central CI fleet), so 24h would force a
// rushed cutover for tenants whose ops team checks the webhook
// receivers once a week. 72h covers a typical "find the change request
// → ship → verify" loop without the rushed-deploy footgun. Compromise
// case: a leaked secret stays usable for up to 72h after the operator
// rotates — bounded but not instant. Tighten to 24h if a tenant-
// configurable cap is justified by a real DP request.
const SecretRotationGracePeriod = 72 * time.Hour

// RotateSecret generates a new signing secret for an endpoint and returns it
// alongside the grace-period expiry. The previous secret stays valid for
// SecretRotationGracePeriod — dispatcher signs outbound events with BOTH
// secrets during the window (two v1= entries in Velox-Signature, Stripe
// multi-signature style) so receivers can stage a verifier update without
// breaking production traffic. After the window, only the new secret is
// used. The new secret is returned once to the caller (dashboard shows it,
// then it's no longer retrievable).
func (s *Service) RotateSecret(ctx context.Context, tenantID, endpointID string) (RotateSecretResult, error) {
	secretBytes := make([]byte, 24)
	if _, err := rand.Read(secretBytes); err != nil {
		return RotateSecretResult{}, fmt.Errorf("generate webhook secret: %w", err)
	}
	newSecret := "whsec_" + hex.EncodeToString(secretBytes)

	ep, err := s.store.RotateEndpointSecret(ctx, tenantID, endpointID, newSecret, SecretRotationGracePeriod)
	if err != nil {
		return RotateSecretResult{}, err
	}
	return RotateSecretResult{
		Secret:             newSecret,
		SecondaryValidTill: ep.SecondarySecretExpiresAt,
	}, nil
}

// RotateSecretResult carries the new secret and the expiry of the
// grace-period sibling. Exposed on the handler response so the dashboard
// can show "old secret valid until <time>" copy.
type RotateSecretResult struct {
	Secret             string     `json:"secret"`
	SecondaryValidTill *time.Time `json:"secondary_valid_until,omitempty"`
}

func (s *Service) ListEndpoints(ctx context.Context, tenantID string) ([]domain.WebhookEndpoint, error) {
	return s.store.ListEndpoints(ctx, tenantID)
}

func (s *Service) DeleteEndpoint(ctx context.Context, tenantID, id string) error {
	return s.store.DeleteEndpoint(ctx, tenantID, id)
}

// Dispatch creates a webhook event and delivers it to all matching endpoints.
//
// Mode scoping: ListEndpoints runs under the caller's ctx livemode, which
// RLS already filters on. The explicit ep.Livemode == event.Livemode check
// below is defense-in-depth — if a future call path opens a bypass tx or
// the RLS predicate is relaxed, test-mode events must still never cross
// into a live endpoint (and vice versa). Cross-mode delivery would leak
// synthetic data into production monitoring.
func (s *Service) Dispatch(ctx context.Context, tenantID, eventType string, payload map[string]any) error {
	event, err := s.store.CreateEvent(ctx, tenantID, domain.WebhookEvent{
		EventType: eventType,
		Payload:   payload,
	})
	if err != nil {
		return fmt.Errorf("create event: %w", err)
	}

	// Publish a "pending" frame to the SSE bus the moment the event row
	// commits — the dashboard renders it immediately, then transitions
	// to "succeeded"/"failed" when the deliver goroutine publishes a
	// follow-up frame after the HTTP attempt completes.
	if s.bus != nil {
		s.bus.Publish(tenantID, FrameFromEvent(event, "pending", nil))
	}

	endpoints, err := s.store.ListEndpoints(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("list endpoints: %w", err)
	}

	for _, ep := range endpoints {
		if !ep.Active {
			continue
		}
		if ep.Livemode != event.Livemode {
			continue
		}
		if !matchesEvent(ep.Events, eventType) {
			continue
		}

		// Pin the event's livemode on the per-delivery ctx so every
		// downstream TxTenant read/write (CreateDelivery, payload
		// builder, signing-secret lookup) lands on the right mode
		// partition. Goroutine path uses a fresh ctx (request may
		// have returned by the time delivery fires) — pinning
		// preserves the event's mode regardless of the request
		// context's lifetime. Sync path inherits the caller ctx but
		// re-pins for the same reason — the caller might have a
		// detached or non-livemode ctx (recipe instantiation, etc).
		dCtx := postgres.WithLivemode(context.Background(), event.Livemode)
		if s.syncDeliver {
			dCtx = postgres.WithLivemode(ctx, event.Livemode)
			s.deliver(dCtx, tenantID, ep, event)
		} else {
			go s.deliver(dCtx, tenantID, ep, event)
		}
	}

	return nil
}

func (s *Service) deliver(ctx context.Context, tenantID string, ep domain.WebhookEndpoint, event domain.WebhookEvent) {
	delivery, err := s.store.CreateDelivery(ctx, tenantID, domain.WebhookDelivery{
		WebhookEndpointID: ep.ID,
		WebhookEventID:    event.ID,
		Status:            domain.DeliveryPending,
	})
	if err != nil {
		slog.Error("create delivery", "error", err)
		return
	}

	// Build payload
	body := map[string]any{
		"id":         event.ID,
		"event_type": event.EventType,
		"created_at": event.CreatedAt.Format(time.RFC3339),
		"data":       event.Payload,
	}
	bodyBytes, _ := json.Marshal(body)

	// Sign with HMAC-SHA256. buildSignatureHeader folds in the grace-
	// period secondary when rotation is in its 72h window.
	now := time.Now().UTC()
	timestamp := fmt.Sprintf("%d", now.Unix())
	sigHeader := buildSignatureHeader(timestamp, bodyBytes, ep, now)

	req, err := http.NewRequestWithContext(ctx, "POST", ep.URL, bytes.NewReader(bodyBytes))
	if err != nil {
		s.scheduleRetryOrFail(ctx, tenantID, delivery, err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Velox-Signature", sigHeader)
	req.Header.Set("Velox-Event-Type", event.EventType)

	resp, err := s.client.Do(req)
	if err != nil {
		s.scheduleRetryOrFail(ctx, tenantID, delivery, err.Error())
		return
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))

	delivery.HTTPStatusCode = resp.StatusCode
	delivery.ResponseBody = string(respBody)

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		now := time.Now().UTC()
		// Count this attempt only on success. scheduleRetryOrFail is the
		// single owner of the increment on the failure path — incrementing
		// here too double-counted, burning a retry and skipping the first
		// backoff interval (retryBackoffs[AttemptCount-1] indexed off the
		// inflated count). Mirrors retryDeliver.
		delivery.AttemptCount++
		delivery.Status = domain.DeliverySucceeded
		delivery.CompletedAt = &now
		delivery.NextRetryAt = nil
	} else {
		s.scheduleRetryOrFail(ctx, tenantID, delivery, fmt.Sprintf("HTTP %d", resp.StatusCode))
		return
	}

	_, _ = s.store.UpdateDelivery(ctx, tenantID, delivery)
	mw.RecordWebhookDelivery("succeeded")

	// Publish the post-attempt frame so the live tail flips the row
	// from "pending" to "succeeded". The dashboard's table is keyed by
	// event_id so the second frame replaces the first in-place.
	if s.bus != nil {
		now := time.Now().UTC()
		s.bus.Publish(tenantID, FrameFromEvent(event, string(delivery.Status), &now))
	}

	slog.Info("webhook delivered",
		"endpoint_id", ep.ID,
		"event_type", event.EventType,
		"status", delivery.Status,
		"http_status", resp.StatusCode,
	)
}

// scheduleRetryOrFail increments the attempt count and either schedules a retry
// with exponential backoff or marks the delivery as permanently failed.
func (s *Service) scheduleRetryOrFail(ctx context.Context, tenantID string, d domain.WebhookDelivery, errMsg string) {
	d.AttemptCount++
	if len(errMsg) > 500 {
		errMsg = errMsg[:500]
	}
	d.ErrorMessage = errMsg

	if d.AttemptCount >= maxAttempts {
		now := time.Now().UTC()
		d.Status = domain.DeliveryFailed
		d.CompletedAt = &now
		d.NextRetryAt = nil
		// Only count permanent failures — per-attempt retries are transient
		// and would drown the alert's success-rate denominator.
		mw.RecordWebhookDelivery("failed")
		// SSE: the dashboard row for this event flips to "failed".
		// Lookup-by-id rather than a struct passthrough so we re-read
		// the event_type/customer_id without bloating the delivery row.
		s.publishFrameForEventID(ctx, tenantID, d.WebhookEventID, "failed", &now)
		slog.Error("webhook delivery permanently failed",
			"delivery_id", d.ID,
			"endpoint_id", d.WebhookEndpointID,
			"attempts", d.AttemptCount,
			"error", errMsg,
		)
	} else {
		jitter := time.Duration(mrand.IntN(30)) * time.Second
		nextRetry := time.Now().UTC().Add(retryBackoffs[d.AttemptCount-1] + jitter)
		d.Status = domain.DeliveryPending
		d.NextRetryAt = &nextRetry
		slog.Warn("webhook delivery scheduled for retry",
			"delivery_id", d.ID,
			"endpoint_id", d.WebhookEndpointID,
			"attempt", d.AttemptCount,
			"next_retry_at", nextRetry.Format(time.RFC3339),
			"error", errMsg,
		)
	}

	_, _ = s.store.UpdateDelivery(ctx, tenantID, d)
}

func matchesEvent(subscribed []string, eventType string) bool {
	for _, s := range subscribed {
		if s == "*" || s == eventType {
			return true
		}
		// Prefix match: "invoice.*" matches "invoice.created"
		if strings.HasSuffix(s, ".*") {
			prefix := strings.TrimSuffix(s, ".*")
			if strings.HasPrefix(eventType, prefix+".") {
				return true
			}
		}
	}
	return false
}

func computeSignature(payload []byte, timestamp, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "." + string(payload)))
	return hex.EncodeToString(mac.Sum(nil))
}

// buildSignatureHeader produces the Velox-Signature header value.
//
// In steady state, it's a single-sig "t=<ts>,v1=<sig>". During a
// grace-period rotation (secondary secret populated and not yet
// expired), it emits two v1= entries — the same multi-signature format
// Stripe uses on Stripe-Signature. Receivers that follow the
// "verify ANY v1= matches" convention pass through the rotation without
// a production outage while their code deploys the new verifier.
//
// `now` is taken as a parameter (rather than called internally) so unit
// tests can exercise pre- and post-expiry branches deterministically.
func buildSignatureHeader(timestamp string, body []byte, ep domain.WebhookEndpoint, now time.Time) string {
	primary := computeSignature(body, timestamp, ep.Secret)
	if ep.SecondarySecret == "" || ep.SecondarySecretExpiresAt == nil || !now.Before(*ep.SecondarySecretExpiresAt) {
		return fmt.Sprintf("t=%s,v1=%s", timestamp, primary)
	}
	secondary := computeSignature(body, timestamp, ep.SecondarySecret)
	return fmt.Sprintf("t=%s,v1=%s,v1=%s", timestamp, primary, secondary)
}

// GetEndpointStats returns delivery success/failure stats per endpoint.
func (s *Service) GetEndpointStats(ctx context.Context, tenantID string) ([]EndpointStats, error) {
	return s.store.GetEndpointStats(ctx, tenantID)
}

// ListEvents returns recent webhook events for a tenant.
func (s *Service) ListEvents(ctx context.Context, tenantID string, limit int) ([]domain.WebhookEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	return s.store.ListEvents(ctx, tenantID, limit)
}

// GetEvent fetches a single event by id (tenant-scoped via RLS at the
// store layer). Surfaced for the SSE handler's deliveries-list path so
// it can resolve the replay root before walking the timeline.
func (s *Service) GetEvent(ctx context.Context, tenantID, id string) (domain.WebhookEvent, error) {
	return s.store.GetEvent(ctx, tenantID, id)
}

// ListDeliveries returns deliveries for a specific event.
func (s *Service) ListDeliveries(ctx context.Context, tenantID, eventID string) ([]domain.WebhookDelivery, error) {
	return s.store.ListDeliveries(ctx, tenantID, eventID)
}

// ReplayResult is the response shape for POST /v1/webhook_events/{id}/replay.
// Returned to the dashboard so the live tail can highlight the new row
// and the Toast confirms what the operator just queued.
type ReplayResult struct {
	// EventID is the freshly-minted webhook_events row that's been
	// queued for delivery. The dashboard's SSE tail will pick it up
	// within a tick.
	EventID string `json:"event_id"`
	// ReplayOf is the ID of the original event whose payload was
	// cloned. The dashboard groups the original + all replays under
	// this pivot so the Deliveries timeline shows the full audit
	// chain.
	ReplayOf string `json:"replay_of"`
	// Status is "queued" — the deliver fan-out runs asynchronously, so
	// at response time we can only confirm the clone landed and
	// matched at least one endpoint.
	Status string `json:"status"`
}

// Replay clones an existing webhook event into a fresh row (with
// replay_of_event_id pointing at the original) and dispatches it to
// every matching active endpoint. The clone is what gets delivered, so
// the original's deliveries are never mutated — every replay produces
// a brand-new row in the timeline. A second replay of the same
// original event is therefore not idempotent in the DB-row sense (it
// creates another clone), but is idempotent in the audit-trail sense:
// the original delivery history is preserved and the operator sees N
// distinct replay attempts on the timeline.
func (s *Service) Replay(ctx context.Context, tenantID, eventID string) (ReplayResult, error) {
	original, err := s.store.GetEvent(ctx, tenantID, eventID)
	if err != nil {
		if errors.Is(err, errs.ErrNotFound) {
			return ReplayResult{}, fmt.Errorf("%w: webhook event", errs.ErrNotFound)
		}
		return ReplayResult{}, fmt.Errorf("get event: %w", err)
	}

	clone, err := s.store.CreateReplayEvent(ctx, tenantID, eventID)
	if err != nil {
		return ReplayResult{}, fmt.Errorf("create replay event: %w", err)
	}

	// Pivot ID: the audit chain points at the *root* original. If the
	// operator clicked Replay on a clone, the store's CreateReplayEvent
	// already collapsed that to root — surface that root in the
	// response so the dashboard groups consistently.
	rootID := eventID
	if clone.ReplayOfEventID != nil && *clone.ReplayOfEventID != "" {
		rootID = *clone.ReplayOfEventID
	}

	// Publish the immediate "pending" frame for the clone so the
	// dashboard shows the new row before the deliver fan-out lands.
	if s.bus != nil {
		s.bus.Publish(tenantID, FrameFromEvent(clone, "pending", nil))
	}

	endpoints, err := s.store.ListEndpoints(ctx, tenantID)
	if err != nil {
		return ReplayResult{}, fmt.Errorf("list endpoints: %w", err)
	}

	for _, ep := range endpoints {
		if !ep.Active || ep.Livemode != clone.Livemode || !matchesEvent(ep.Events, clone.EventType) {
			continue
		}
		// Pin the event's livemode on the delivery ctx, exactly as Dispatch
		// does. The goroutine path starts from context.Background() (the
		// replay request may have returned before delivery fires), and a
		// bare ctx makes postgres.Livemode default to TRUE — so CreateDelivery
		// / UpdateDelivery would write a test-mode replay's rows into the LIVE
		// RLS partition (invisible in the test-mode dashboard, FK to a
		// test-mode event broken). The sync path re-pins too: the caller ctx
		// might carry the wrong mode. Mirrors Dispatch's pin above.
		dCtx := postgres.WithLivemode(context.Background(), clone.Livemode)
		if s.syncDeliver {
			s.deliver(postgres.WithLivemode(ctx, clone.Livemode), tenantID, ep, clone)
		} else {
			go s.deliver(dCtx, tenantID, ep, clone)
		}
	}

	slog.Info("webhook event replayed",
		"event_id", clone.ID,
		"replay_of", rootID,
		"event_type", clone.EventType,
	)
	_ = original // referenced for not-found semantics above
	return ReplayResult{
		EventID:  clone.ID,
		ReplayOf: rootID,
		Status:   "queued",
	}, nil
}

// RetryPendingDeliveries picks up deliveries due for retry and re-attempts them.
func (s *Service) RetryPendingDeliveries(ctx context.Context) error {
	deliveries, err := s.store.ListPendingDeliveries(ctx, 100)
	if err != nil {
		return fmt.Errorf("list pending deliveries: %w", err)
	}

	if len(deliveries) == 0 {
		return nil
	}

	slog.Info("retrying webhook deliveries", "count", len(deliveries))

	for _, d := range deliveries {
		// ListPendingDeliveries runs in TxBypass (cross-tenant), so we tag
		// the per-delivery ctx with the row's livemode. Every downstream
		// store call opens its own TxTenant and needs this to route the
		// delivery back to the same mode partition.
		dCtx := postgres.WithLivemode(ctx, d.Livemode)

		ep, err := s.store.GetEndpoint(dCtx, d.TenantID, d.WebhookEndpointID)
		if err != nil {
			slog.Error("get endpoint for retry", "delivery_id", d.ID, "error", err)
			continue
		}
		if !ep.Active {
			// Endpoint was disabled; mark delivery as failed.
			now := time.Now().UTC()
			d.Status = domain.DeliveryFailed
			d.ErrorMessage = "endpoint disabled"
			d.CompletedAt = &now
			d.NextRetryAt = nil
			_, _ = s.store.UpdateDelivery(dCtx, d.TenantID, d)
			continue
		}

		// Point-lookup the event directly. The previous ListEvents+scan
		// clamped to the newest ~100 rows, so older pending deliveries
		// (whose event had aged out of that window) were stranded forever.
		event, err := s.store.GetEvent(dCtx, d.TenantID, d.WebhookEventID)
		if err != nil {
			if errors.Is(err, errs.ErrNotFound) {
				// Event genuinely deleted — the delivery can never succeed.
				// Mark it failed so it stops cycling through the retry pool.
				now := time.Now().UTC()
				d.Status = domain.DeliveryFailed
				d.ErrorMessage = "event not found"
				d.CompletedAt = &now
				d.NextRetryAt = nil
				_, _ = s.store.UpdateDelivery(dCtx, d.TenantID, d)
				slog.Error("event not found for retry", "delivery_id", d.ID, "event_id", d.WebhookEventID)
				continue
			}
			slog.Error("get event for retry", "delivery_id", d.ID, "event_id", d.WebhookEventID, "error", err)
			continue
		}

		s.retryDeliver(dCtx, d, ep, event)
	}

	return nil
}

// retryDeliver re-attempts an existing delivery (does not create a new delivery row).
func (s *Service) retryDeliver(ctx context.Context, d domain.WebhookDelivery, ep domain.WebhookEndpoint, event domain.WebhookEvent) {
	body := map[string]any{
		"id":         event.ID,
		"event_type": event.EventType,
		"created_at": event.CreatedAt.Format(time.RFC3339),
		"data":       event.Payload,
	}
	bodyBytes, _ := json.Marshal(body)

	now := time.Now().UTC()
	timestamp := fmt.Sprintf("%d", now.Unix())
	sigHeader := buildSignatureHeader(timestamp, bodyBytes, ep, now)

	req, err := http.NewRequestWithContext(ctx, "POST", ep.URL, bytes.NewReader(bodyBytes))
	if err != nil {
		s.scheduleRetryOrFail(ctx, d.TenantID, d, err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Velox-Signature", sigHeader)
	req.Header.Set("Velox-Event-Type", event.EventType)

	resp, err := s.client.Do(req)
	if err != nil {
		s.scheduleRetryOrFail(ctx, d.TenantID, d, err.Error())
		return
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))

	d.HTTPStatusCode = resp.StatusCode
	d.ResponseBody = string(respBody)

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		now := time.Now().UTC()
		d.AttemptCount++
		d.Status = domain.DeliverySucceeded
		d.CompletedAt = &now
		d.NextRetryAt = nil
		_, _ = s.store.UpdateDelivery(ctx, d.TenantID, d)
		slog.Info("webhook retry succeeded",
			"delivery_id", d.ID,
			"endpoint_id", ep.ID,
			"attempt", d.AttemptCount,
		)
	} else {
		s.scheduleRetryOrFail(ctx, d.TenantID, d, fmt.Sprintf("HTTP %d", resp.StatusCode))
	}
}

// StartRetryWorker runs a background loop that retries pending deliveries on
// the given interval. It blocks until the context is cancelled.
func (s *Service) StartRetryWorker(ctx context.Context, interval time.Duration) {
	slog.Info("webhook retry worker started", "interval", interval.String())
	scheduler.Run(ctx, "webhook_retry", interval, func(ctx context.Context) {
		if err := s.RetryPendingDeliveries(ctx); err != nil {
			slog.Error("webhook retry worker error", "error", err)
		}
	})
}
