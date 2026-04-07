package tenant

import (
	"context"

	"github.com/sagarsuperuser/velox/internal/domain"
)

type Store interface {
	Create(ctx context.Context, t domain.Tenant) (domain.Tenant, error)
	Get(ctx context.Context, id string) (domain.Tenant, error)
	List(ctx context.Context, filter ListFilter) ([]domain.Tenant, error)
	UpdateStatus(ctx context.Context, id string, status domain.TenantStatus) (domain.Tenant, error)
}

type ListFilter struct {
	Status string
	Limit  int
	Offset int
}
