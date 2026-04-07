package customer

import (
	"context"

	"github.com/sagarsuperuser/velox/internal/domain"
)

type Store interface {
	Create(ctx context.Context, tenantID string, c domain.Customer) (domain.Customer, error)
	Get(ctx context.Context, tenantID, id string) (domain.Customer, error)
	GetByExternalID(ctx context.Context, tenantID, externalID string) (domain.Customer, error)
	List(ctx context.Context, filter ListFilter) ([]domain.Customer, int, error)
	Update(ctx context.Context, tenantID string, c domain.Customer) (domain.Customer, error)

	UpsertBillingProfile(ctx context.Context, tenantID string, bp domain.CustomerBillingProfile) (domain.CustomerBillingProfile, error)
	GetBillingProfile(ctx context.Context, tenantID, customerID string) (domain.CustomerBillingProfile, error)

	UpsertPaymentSetup(ctx context.Context, tenantID string, ps domain.CustomerPaymentSetup) (domain.CustomerPaymentSetup, error)
	GetPaymentSetup(ctx context.Context, tenantID, customerID string) (domain.CustomerPaymentSetup, error)
}

type ListFilter struct {
	TenantID   string
	Status     string
	ExternalID string
	Limit      int
	Offset     int
}
