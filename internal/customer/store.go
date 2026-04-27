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

	// MarkEmailBounced records a permanent delivery failure for a customer.
	// Sender calls this via a narrow interface when SMTP returns a 5xx; the
	// same path is later reused by provider webhooks (SES/SendGrid) when
	// wired. Idempotent — repeated calls just refresh the timestamp.
	MarkEmailBounced(ctx context.Context, tenantID, customerID, reason string) error

	// SetCostDashboardToken writes (or rotates) the cost-dashboard URL
	// token for one customer. Empty token clears the column (parking
	// the customer back to "no public URL"). The store enforces the
	// partial UNIQUE index from migration 0064 — collisions surface as
	// an error rather than a silent overwrite.
	SetCostDashboardToken(ctx context.Context, tenantID, customerID, token string) error

	// GetByCostDashboardToken resolves a token to its customer with no
	// tenant context — the public iframe handler hits this BEFORE it
	// knows which tenant to scope to. Implementations MUST run under
	// TxBypass so the lookup spans every tenant; the 256-bit token
	// itself is the credential.
	GetByCostDashboardToken(ctx context.Context, token string) (domain.Customer, error)
}

type ListFilter struct {
	TenantID   string
	Status     string
	ExternalID string
	Limit      int
	Offset     int
}
