package tenant

import (
	"context"
	"database/sql"

	"github.com/sagarsuperuser/velox/internal/domain"
)

type Store interface {
	Create(ctx context.Context, t domain.Tenant) (domain.Tenant, error)
	// CreateAudited runs the caller-supplied audit emission in the same
	// transaction as the tenants INSERT (ADR-090 shared fate); the tx is
	// opened AS the new tenant so the FORCE-RLS'd audit_log accepts the row.
	CreateAudited(ctx context.Context, t domain.Tenant, emit func(tx *sql.Tx, out domain.Tenant) error) (domain.Tenant, error)
	Get(ctx context.Context, id string) (domain.Tenant, error)
	List(ctx context.Context, filter ListFilter) ([]domain.Tenant, error)
	UpdateStatus(ctx context.Context, id string, status domain.TenantStatus) (domain.Tenant, error)
}

type ListFilter struct {
	Status string
	Limit  int
	Offset int
}
