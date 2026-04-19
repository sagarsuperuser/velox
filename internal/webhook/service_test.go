package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
)

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

type memStore struct {
	endpoints  map[string]domain.WebhookEndpoint
	events     []domain.WebhookEvent
	deliveries []domain.WebhookDelivery
}

func newMemStore() *memStore {
	return &memStore{endpoints: make(map[string]domain.WebhookEndpoint)}
}

func (m *memStore) CreateEndpoint(_ context.Context, tenantID string, ep domain.WebhookEndpoint) (domain.WebhookEndpoint, error) {
	ep.ID = fmt.Sprintf("vlx_whe_%d", len(m.endpoints)+1)
	ep.TenantID = tenantID
	ep.CreatedAt = time.Now().UTC()
	ep.UpdatedAt = ep.CreatedAt
	ep.SecretLast4 = lastFour(ep.Secret)
	m.endpoints[ep.ID] = ep
	return ep, nil
}

func (m *memStore) GetEndpoint(_ context.Context, tenantID, id string) (domain.WebhookEndpoint, error) {
	ep, ok := m.endpoints[id]
	if !ok || ep.TenantID != tenantID {
		return domain.WebhookEndpoint{}, errs.ErrNotFound
	}
	return ep, nil
}

func (m *memStore) ListEndpoints(_ context.Context, tenantID string) ([]domain.WebhookEndpoint, error) {
	var result []domain.WebhookEndpoint
	for _, ep := range m.endpoints {
		if ep.TenantID == tenantID && ep.Active {
			result = append(result, ep)
		}
	}
	return result, nil
}

func (m *memStore) DeleteEndpoint(_ context.Context, tenantID, id string) error {
	ep, ok := m.endpoints[id]
	if !ok || ep.TenantID != tenantID {
		return errs.ErrNotFound
	}
	delete(m.endpoints, id)
	return nil
}

func (m *memStore) UpdateEndpointSecret(_ context.Context, tenantID, id, newSecret string) (domain.WebhookEndpoint, error) {
	ep, ok := m.endpoints[id]
	if !ok || ep.TenantID != tenantID {
		return domain.WebhookEndpoint{}, errs.ErrNotFound
	}
	ep.Secret = newSecret
	ep.SecretLast4 = lastFour(newSecret)
	ep.UpdatedAt = time.Now().UTC()
	m.endpoints[id] = ep
	return ep, nil
}

func (m *memStore) CreateEvent(_ context.Context, tenantID string, event domain.WebhookEvent) (domain.WebhookEvent, error) {
	event.ID = fmt.Sprintf("vlx_whevt_%d", len(m.events)+1)
	event.TenantID = tenantID
	event.CreatedAt = time.Now().UTC()
	m.events = append(m.events, event)
	return event, nil
}

func (m *memStore) ListEvents(_ context.Context, tenantID string, limit int) ([]domain.WebhookEvent, error) {
	var result []domain.WebhookEvent
	for _, e := range m.events {
		if e.TenantID == tenantID {
			result = append(result, e)
		}
	}
	return result, nil
}

func (m *memStore) CreateDelivery(_ context.Context, tenantID string, d domain.WebhookDelivery) (domain.WebhookDelivery, error) {
	d.ID = fmt.Sprintf("vlx_whd_%d", len(m.deliveries)+1)
	d.TenantID = tenantID
	d.CreatedAt = time.Now().UTC()
	m.deliveries = append(m.deliveries, d)
	return d, nil
}

func (m *memStore) UpdateDelivery(_ context.Context, _ string, d domain.WebhookDelivery) (domain.WebhookDelivery, error) {
	for i, existing := range m.deliveries {
		if existing.ID == d.ID {
			m.deliveries[i] = d
			return d, nil
		}
	}
	return d, nil
}

func (m *memStore) ListDeliveries(_ context.Context, _, eventID string) ([]domain.WebhookDelivery, error) {
	var result []domain.WebhookDelivery
	for _, d := range m.deliveries {
		if d.WebhookEventID == eventID {
			result = append(result, d)
		}
	}
	return result, nil
}

func (m *memStore) GetEndpointStats(_ context.Context, tenantID string) ([]EndpointStats, error) {
	counts := make(map[string]*EndpointStats)
	for _, d := range m.deliveries {
		if d.TenantID != tenantID {
			continue
		}
		s, ok := counts[d.WebhookEndpointID]
		if !ok {
			s = &EndpointStats{EndpointID: d.WebhookEndpointID}
			counts[d.WebhookEndpointID] = s
		}
		s.TotalDeliveries++
		if d.Status == domain.DeliverySucceeded { //nolint:staticcheck
			s.Succeeded++
		} else if d.Status == domain.DeliveryFailed {
			s.Failed++
		}
	}
	var result []EndpointStats
	for _, s := range counts {
		if s.TotalDeliveries > 0 {
			s.SuccessRate = float64(s.Succeeded) / float64(s.TotalDeliveries) * 100
		}
		result = append(result, *s)
	}
	return result, nil
}

func (m *memStore) ListPendingDeliveries(_ context.Context, limit int) ([]domain.WebhookDelivery, error) {
	var result []domain.WebhookDelivery
	now := time.Now()
	for _, d := range m.deliveries {
		if d.Status == domain.DeliveryPending && d.NextRetryAt != nil && !d.NextRetryAt.After(now) {
			result = append(result, d)
			if len(result) >= limit {
				break
			}
		}
	}
	return result, nil
}

// mockHTTPClient captures requests and returns configurable responses.
type mockHTTPClient struct {
	lastRequest *http.Request
	lastBody    []byte
	statusCode  int
}

func (m *mockHTTPClient) Do(req *http.Request) (*http.Response, error) {
	m.lastRequest = req
	body, _ := io.ReadAll(req.Body)
	m.lastBody = body
	return &http.Response{
		StatusCode: m.statusCode,
		Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
	}, nil
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestCreateEndpoint(t *testing.T) {
	svc := NewService(newMemStore(), nil)
	ctx := context.Background()

	t.Run("valid localhost", func(t *testing.T) {
		result, err := svc.CreateEndpoint(ctx, "t1", CreateEndpointInput{
			URL:    "http://localhost:8080/webhooks",
			Events: []string{"invoice.created", "payment.succeeded"},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.HasPrefix(result.Secret, "whsec_") {
			t.Errorf("secret should start with whsec_, got %q", result.Secret[:10])
		}
		if result.Endpoint.URL != "http://localhost:8080/webhooks" {
			t.Errorf("url: got %q", result.Endpoint.URL)
		}
		if len(result.Endpoint.Events) != 2 {
			t.Errorf("events: got %d, want 2", len(result.Endpoint.Events))
		}
		if result.Endpoint.SecretLast4 != result.Secret[len(result.Secret)-4:] {
			t.Errorf("secret_last4: got %q, want %q (last 4 of %q)",
				result.Endpoint.SecretLast4, result.Secret[len(result.Secret)-4:], result.Secret)
		}
	})

	t.Run("private IP blocked", func(t *testing.T) {
		_, err := svc.CreateEndpoint(ctx, "t1", CreateEndpointInput{URL: "https://10.0.0.1/hook"})
		if err == nil {
			t.Fatal("expected error for private IP URL")
		}
		if !strings.Contains(err.Error(), "private/internal IP") {
			t.Errorf("expected private IP error, got: %v", err)
		}
	})

	t.Run("missing url", func(t *testing.T) {
		_, err := svc.CreateEndpoint(ctx, "t1", CreateEndpointInput{})
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("non-https rejected", func(t *testing.T) {
		_, err := svc.CreateEndpoint(ctx, "t1", CreateEndpointInput{URL: "http://api.acme.com/webhooks"})
		if err == nil {
			t.Fatal("expected error for non-HTTPS URL")
		}
	})

	t.Run("localhost allowed", func(t *testing.T) {
		_, err := svc.CreateEndpoint(ctx, "t1", CreateEndpointInput{URL: "http://localhost:3000/webhooks"})
		if err != nil {
			t.Fatalf("localhost should be allowed: %v", err)
		}
	})

	t.Run("default wildcard events", func(t *testing.T) {
		result, _ := svc.CreateEndpoint(ctx, "t1", CreateEndpointInput{URL: "http://localhost:8080/hook"})
		if len(result.Endpoint.Events) != 1 || result.Endpoint.Events[0] != "*" {
			t.Errorf("default events should be [*], got %v", result.Endpoint.Events)
		}
	})
}

func TestDispatch(t *testing.T) {
	store := newMemStore()
	httpClient := &mockHTTPClient{statusCode: 200}
	svc := NewTestService(store, httpClient)
	ctx := context.Background()

	// Register an endpoint (localhost to bypass SSRF DNS check in tests)
	result, _ := svc.CreateEndpoint(ctx, "t1", CreateEndpointInput{
		URL:    "http://localhost:9999/webhooks",
		Events: []string{"invoice.created"},
	})

	// Dispatch an event
	err := svc.Dispatch(ctx, "t1", "invoice.created", map[string]any{
		"invoice_id": "inv_123",
		"total":      19900,
	})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	// Give async delivery goroutine time to complete
	// Delivery is synchronous in test mode

	// Verify event was created
	if len(store.events) != 1 {
		t.Fatalf("events: got %d, want 1", len(store.events))
	}
	if store.events[0].EventType != "invoice.created" {
		t.Errorf("event_type: got %q", store.events[0].EventType)
	}

	// Verify delivery was made
	if len(store.deliveries) != 1 {
		t.Fatalf("deliveries: got %d, want 1", len(store.deliveries))
	}
	if store.deliveries[0].Status != domain.DeliverySucceeded {
		t.Errorf("delivery status: got %q, want succeeded", store.deliveries[0].Status)
	}

	// Verify HMAC signature was sent
	if httpClient.lastRequest != nil {
		sig := httpClient.lastRequest.Header.Get("Velox-Signature")
		if !strings.Contains(sig, "t=") || !strings.Contains(sig, "v1=") {
			t.Errorf("missing signature header, got %q", sig)
		}

		// Verify signature is valid
		parts := strings.Split(sig, ",")
		var ts, v1 string
		for _, p := range parts {
			kv := strings.SplitN(p, "=", 2)
			if kv[0] == "t" {
				ts = kv[1]
			}
			if kv[0] == "v1" {
				v1 = kv[1]
			}
		}

		mac := hmac.New(sha256.New, []byte(result.Secret))
		mac.Write([]byte(ts + "." + string(httpClient.lastBody)))
		expected := hex.EncodeToString(mac.Sum(nil))
		if v1 != expected {
			t.Error("HMAC signature mismatch")
		}
	}

	// Verify Velox-Event-Type header
	if httpClient.lastRequest != nil {
		et := httpClient.lastRequest.Header.Get("Velox-Event-Type")
		if et != "invoice.created" {
			t.Errorf("Velox-Event-Type: got %q", et)
		}
	}

	// Verify payload structure
	if httpClient.lastBody != nil {
		var payload map[string]any
		_ = json.Unmarshal(httpClient.lastBody, &payload)
		if payload["event_type"] != "invoice.created" {
			t.Errorf("payload event_type: got %v", payload["event_type"])
		}
		data, _ := payload["data"].(map[string]any)
		if data["invoice_id"] != "inv_123" {
			t.Errorf("payload data.invoice_id: got %v", data["invoice_id"])
		}
	}
}

func TestDispatch_NonMatchingEvent(t *testing.T) {
	store := newMemStore()
	httpClient := &mockHTTPClient{statusCode: 200}
	svc := NewTestService(store, httpClient)
	ctx := context.Background()

	// Register endpoint for invoice events only
	_, _ = svc.CreateEndpoint(ctx, "t1", CreateEndpointInput{
		URL:    "http://localhost:9999/hook",
		Events: []string{"invoice.*"},
	})

	// Dispatch a payment event — should NOT trigger delivery
	_ = svc.Dispatch(ctx, "t1", "payment.succeeded", map[string]any{})

	// Delivery is synchronous in test mode

	if len(store.deliveries) != 0 {
		t.Errorf("no delivery expected for non-matching event, got %d", len(store.deliveries))
	}
}

func TestDispatch_WildcardEndpoint(t *testing.T) {
	store := newMemStore()
	httpClient := &mockHTTPClient{statusCode: 200}
	svc := NewTestService(store, httpClient)
	ctx := context.Background()

	// Register endpoint for all events
	_, _ = svc.CreateEndpoint(ctx, "t1", CreateEndpointInput{
		URL:    "http://localhost:9999/hook",
		Events: []string{"*"},
	})

	_ = svc.Dispatch(ctx, "t1", "dunning.started", map[string]any{})
	// Delivery is synchronous in test mode

	if len(store.deliveries) != 1 {
		t.Errorf("wildcard should match any event, got %d deliveries", len(store.deliveries))
	}
}

func TestMatchesEvent(t *testing.T) {
	tests := []struct {
		subscribed []string
		event      string
		want       bool
	}{
		{[]string{"*"}, "invoice.created", true},
		{[]string{"invoice.created"}, "invoice.created", true},
		{[]string{"invoice.created"}, "payment.succeeded", false},
		{[]string{"invoice.*"}, "invoice.created", true},
		{[]string{"invoice.*"}, "invoice.finalized", true},
		{[]string{"invoice.*"}, "payment.succeeded", false},
		{[]string{"invoice.created", "payment.succeeded"}, "payment.succeeded", true},
		{[]string{}, "anything", false},
	}

	for _, tt := range tests {
		got := matchesEvent(tt.subscribed, tt.event)
		if got != tt.want {
			t.Errorf("matchesEvent(%v, %q) = %v, want %v", tt.subscribed, tt.event, got, tt.want)
		}
	}
}

func TestDelivery_FailedHTTP(t *testing.T) {
	store := newMemStore()
	httpClient := &mockHTTPClient{statusCode: 500}
	svc := NewTestService(store, httpClient)
	ctx := context.Background()

	_, _ = svc.CreateEndpoint(ctx, "t1", CreateEndpointInput{URL: "http://localhost:9999/hook"})
	_ = svc.Dispatch(ctx, "t1", "invoice.created", map[string]any{})
	// Delivery is synchronous in test mode

	if len(store.deliveries) != 1 {
		t.Fatalf("deliveries: got %d", len(store.deliveries))
	}
	// First failure schedules a retry (status stays pending)
	if store.deliveries[0].Status != domain.DeliveryPending {
		t.Errorf("status: got %q, want pending (scheduled for retry)", store.deliveries[0].Status)
	}
	if store.deliveries[0].AttemptCount != 2 {
		t.Errorf("attempt_count: got %d, want 2", store.deliveries[0].AttemptCount)
	}
	if store.deliveries[0].NextRetryAt == nil {
		t.Error("next_retry_at should be set for retry")
	}
}

func TestReplay(t *testing.T) {
	store := newMemStore()
	httpClient := &mockHTTPClient{statusCode: 200}
	svc := NewTestService(store, httpClient)
	ctx := context.Background()

	// Setup: create endpoint + dispatch event
	_, _ = svc.CreateEndpoint(ctx, "t1", CreateEndpointInput{
		URL:    "http://localhost:9999/hook",
		Events: []string{"invoice.created"},
	})
	_ = svc.Dispatch(ctx, "t1", "invoice.created", map[string]any{"id": "inv_1"})

	if len(store.deliveries) != 1 {
		t.Fatalf("initial deliveries: got %d, want 1", len(store.deliveries))
	}

	// Replay the event
	eventID := store.events[0].ID
	err := svc.Replay(ctx, "t1", eventID)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}

	// Should have 2 deliveries now (original + replay)
	if len(store.deliveries) != 2 {
		t.Errorf("deliveries after replay: got %d, want 2", len(store.deliveries))
	}
}

func TestReplay_NotFound(t *testing.T) {
	svc := NewTestService(newMemStore(), &mockHTTPClient{statusCode: 200})

	err := svc.Replay(context.Background(), "t1", "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent event")
	}
}
