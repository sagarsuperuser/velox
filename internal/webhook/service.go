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
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
)

type Service struct {
	store      Store
	client     HTTPClient
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
		return CreateEndpointResult{}, fmt.Errorf("url is required")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return CreateEndpointResult{}, fmt.Errorf("url must be a valid URL")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && strings.HasPrefix(parsed.Host, "localhost")) {
		return CreateEndpointResult{}, fmt.Errorf("webhook URL must use HTTPS (except localhost)")
	}

	events := input.Events
	if len(events) == 0 {
		events = []string{"*"}
	}

	// Generate signing secret
	secretBytes := make([]byte, 24)
	rand.Read(secretBytes)
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

func (s *Service) ListEndpoints(ctx context.Context, tenantID string) ([]domain.WebhookEndpoint, error) {
	return s.store.ListEndpoints(ctx, tenantID)
}

func (s *Service) DeleteEndpoint(ctx context.Context, tenantID, id string) error {
	return s.store.DeleteEndpoint(ctx, tenantID, id)
}

// Dispatch creates a webhook event and delivers it to all matching endpoints.
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

	// Sign with HMAC-SHA256
	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	signature := computeSignature(bodyBytes, timestamp, ep.Secret)

	req, err := http.NewRequestWithContext(ctx, "POST", ep.URL, bytes.NewReader(bodyBytes))
	if err != nil {
		s.recordFailure(ctx, tenantID, delivery, err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Velox-Signature", fmt.Sprintf("t=%s,v1=%s", timestamp, signature))
	req.Header.Set("Velox-Event-Type", event.EventType)

	resp, err := s.client.Do(req)
	if err != nil {
		s.recordFailure(ctx, tenantID, delivery, err.Error())
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))

	now := time.Now().UTC()
	delivery.HTTPStatusCode = resp.StatusCode
	delivery.ResponseBody = string(respBody)
	delivery.AttemptCount = 1
	delivery.CompletedAt = &now

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		delivery.Status = domain.DeliverySucceeded
	} else {
		delivery.Status = domain.DeliveryFailed
		delivery.ErrorMessage = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}

	s.store.UpdateDelivery(ctx, tenantID, delivery)

	slog.Info("webhook delivered",
		"endpoint_id", ep.ID,
		"event_type", event.EventType,
		"status", delivery.Status,
		"http_status", resp.StatusCode,
	)
}

func (s *Service) recordFailure(ctx context.Context, tenantID string, d domain.WebhookDelivery, errMsg string) {
	now := time.Now().UTC()
	d.Status = domain.DeliveryFailed
	d.ErrorMessage = errMsg
	d.AttemptCount = 1
	d.CompletedAt = &now
	s.store.UpdateDelivery(ctx, tenantID, d)

	slog.Error("webhook delivery failed",
		"endpoint_id", d.WebhookEndpointID,
		"error", errMsg,
	)
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
		if !ep.Active || !matchesEvent(ep.Events, event.EventType) {
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
