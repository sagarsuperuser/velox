package tenant

import (
	"context"
	"strings"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
)

type Service struct {
	store Store
}

func NewService(store Store) *Service {
	return &Service{store: store}
}

type CreateInput struct {
	Name string `json:"name"`
}

func (s *Service) Create(ctx context.Context, input CreateInput) (domain.Tenant, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return domain.Tenant{}, errs.Required("name")
	}

	return s.store.Create(ctx, domain.Tenant{Name: name})
}

func (s *Service) Get(ctx context.Context, id string) (domain.Tenant, error) {
	if id == "" {
		return domain.Tenant{}, errs.Required("id")
	}
	return s.store.Get(ctx, id)
}

func (s *Service) List(ctx context.Context, filter ListFilter) ([]domain.Tenant, error) {
	return s.store.List(ctx, filter)
}

func (s *Service) UpdateStatus(ctx context.Context, id string, status domain.TenantStatus) (domain.Tenant, error) {
	if id == "" {
		return domain.Tenant{}, errs.Required("id")
	}
	return s.store.UpdateStatus(ctx, id, status)
}
