package webhook

import (
	"context"

	"github.com/sagarsuperuser/velox/internal/domain"
)

type Store interface {
	// Endpoints
	CreateEndpoint(ctx context.Context, tenantID string, ep domain.WebhookEndpoint) (domain.WebhookEndpoint, error)
	GetEndpoint(ctx context.Context, tenantID, id string) (domain.WebhookEndpoint, error)
	ListEndpoints(ctx context.Context, tenantID string) ([]domain.WebhookEndpoint, error)
	DeleteEndpoint(ctx context.Context, tenantID, id string) error

	// Events
	CreateEvent(ctx context.Context, tenantID string, event domain.WebhookEvent) (domain.WebhookEvent, error)
	ListEvents(ctx context.Context, tenantID string, limit int) ([]domain.WebhookEvent, error)

	// Deliveries
	CreateDelivery(ctx context.Context, tenantID string, d domain.WebhookDelivery) (domain.WebhookDelivery, error)
	UpdateDelivery(ctx context.Context, tenantID string, d domain.WebhookDelivery) (domain.WebhookDelivery, error)
	ListDeliveries(ctx context.Context, tenantID, eventID string) ([]domain.WebhookDelivery, error)
}
