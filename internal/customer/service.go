package customer

import (
	"context"
	"fmt"
	"strings"

	"github.com/sagarsuperuser/velox/internal/domain"
)

type Service struct {
	store Store
}

func NewService(store Store) *Service {
	return &Service{store: store}
}

type CreateInput struct {
	ExternalID  string `json:"external_id"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email,omitempty"`
}

func (s *Service) Create(ctx context.Context, tenantID string, input CreateInput) (domain.Customer, error) {
	input.ExternalID = strings.TrimSpace(input.ExternalID)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.Email = strings.TrimSpace(input.Email)

	if input.ExternalID == "" {
		return domain.Customer{}, fmt.Errorf("external_id is required")
	}
	if input.DisplayName == "" {
		return domain.Customer{}, fmt.Errorf("display_name is required")
	}

	return s.store.Create(ctx, tenantID, domain.Customer{
		ExternalID:  input.ExternalID,
		DisplayName: input.DisplayName,
		Email:       input.Email,
	})
}

func (s *Service) Get(ctx context.Context, tenantID, id string) (domain.Customer, error) {
	return s.store.Get(ctx, tenantID, id)
}

func (s *Service) List(ctx context.Context, filter ListFilter) ([]domain.Customer, int, error) {
	return s.store.List(ctx, filter)
}

type UpdateInput struct {
	DisplayName string `json:"display_name"`
	Email       string `json:"email,omitempty"`
	Status      string `json:"status,omitempty"`
}

func (s *Service) Update(ctx context.Context, tenantID, id string, input UpdateInput) (domain.Customer, error) {
	existing, err := s.store.Get(ctx, tenantID, id)
	if err != nil {
		return domain.Customer{}, err
	}

	if name := strings.TrimSpace(input.DisplayName); name != "" {
		existing.DisplayName = name
	}
	if email := strings.TrimSpace(input.Email); email != "" {
		existing.Email = email
	}
	if status := domain.CustomerStatus(input.Status); status != "" {
		existing.Status = status
	}

	return s.store.Update(ctx, tenantID, existing)
}

func (s *Service) UpsertBillingProfile(ctx context.Context, tenantID string, bp domain.CustomerBillingProfile) (domain.CustomerBillingProfile, error) {
	if bp.CustomerID == "" {
		return domain.CustomerBillingProfile{}, fmt.Errorf("customer_id is required")
	}
	if bp.ProfileStatus == "" {
		bp.ProfileStatus = domain.BillingProfileIncomplete
	}
	return s.store.UpsertBillingProfile(ctx, tenantID, bp)
}

func (s *Service) GetBillingProfile(ctx context.Context, tenantID, customerID string) (domain.CustomerBillingProfile, error) {
	return s.store.GetBillingProfile(ctx, tenantID, customerID)
}
