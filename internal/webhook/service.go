package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
)

const maxAttempts = 5

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
	syncDeliver bool // When true, deliver synchronously (for tests)
}

// HTTPClient is the interface for making HTTP requests (mockable in tests).
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

func NewService(store Store, client HTTPClient) *Service {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &Service{store: store, client: client}
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

// SecretRotationGracePeriod is how long a rotated-out secret keeps
// signing alongside its replacement. Matches Stripe's public guidance
// for their rotating signing-secret feature: long enough for a partner
// to stage a deploy across a typical release window, short enough that a
// compromised-key rotation has a bounded bleed-through.
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

		if s.syncDeliver {
			s.deliver(ctx, tenantID, ep, event)
		} else {
			go s.deliver(context.Background(), tenantID, ep, event)
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
	delivery.AttemptCount++

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		now := time.Now().UTC()
		delivery.Status = domain.DeliverySucceeded
		delivery.CompletedAt = &now
		delivery.NextRetryAt = nil
	} else {
		s.scheduleRetryOrFail(ctx, tenantID, delivery, fmt.Sprintf("HTTP %d", resp.StatusCode))
		return
	}

	_, _ = s.store.UpdateDelivery(ctx, tenantID, delivery)
	mw.RecordWebhookDelivery("succeeded")

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

// ListDeliveries returns deliveries for a specific event.
func (s *Service) ListDeliveries(ctx context.Context, tenantID, eventID string) ([]domain.WebhookDelivery, error) {
	return s.store.ListDeliveries(ctx, tenantID, eventID)
}

// Replay re-delivers a webhook event to all matching active endpoints.
func (s *Service) Replay(ctx context.Context, tenantID, eventID string) error {
	events, err := s.store.ListEvents(ctx, tenantID, 1000)
	if err != nil {
		return err
	}

	var event *domain.WebhookEvent
	for i := range events {
		if events[i].ID == eventID {
			event = &events[i]
			break
		}
	}
	if event == nil {
		return fmt.Errorf("%w: webhook event", errs.ErrNotFound)
	}

	endpoints, err := s.store.ListEndpoints(ctx, tenantID)
	if err != nil {
		return err
	}

	for _, ep := range endpoints {
		if !ep.Active || ep.Livemode != event.Livemode || !matchesEvent(ep.Events, event.EventType) {
			continue
		}
		if s.syncDeliver {
			s.deliver(ctx, tenantID, ep, *event)
		} else {
			go s.deliver(context.Background(), tenantID, ep, *event)
		}
	}

	slog.Info("webhook event replayed", "event_id", eventID, "event_type", event.EventType)
	return nil
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

		events, err := s.store.ListEvents(dCtx, d.TenantID, 1000)
		if err != nil {
			slog.Error("list events for retry", "delivery_id", d.ID, "error", err)
			continue
		}
		var event *domain.WebhookEvent
		for i := range events {
			if events[i].ID == d.WebhookEventID {
				event = &events[i]
				break
			}
		}
		if event == nil {
			slog.Error("event not found for retry", "delivery_id", d.ID, "event_id", d.WebhookEventID)
			continue
		}

		s.retryDeliver(dCtx, d, ep, *event)
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
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	slog.Info("webhook retry worker started", "interval", interval.String())

	for {
		select {
		case <-ctx.Done():
			slog.Info("webhook retry worker stopped")
			return
		case <-ticker.C:
			if err := s.RetryPendingDeliveries(ctx); err != nil {
				slog.Error("webhook retry worker error", "error", err)
			}
		}
	}
}
