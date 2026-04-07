package auth

import (
	"context"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
)

type Store interface {
	Create(ctx context.Context, key domain.APIKey) (domain.APIKey, error)
	GetByPrefix(ctx context.Context, prefix string) (domain.APIKey, error)
	Revoke(ctx context.Context, tenantID, id string) (domain.APIKey, error)
	List(ctx context.Context, filter ListFilter) ([]domain.APIKey, error)
	TouchLastUsed(ctx context.Context, id string, usedAt time.Time) error
}

type ListFilter struct {
	TenantID string
	Role     string
	Limit    int
	Offset   int
}
