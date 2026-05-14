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
	// ListByTestClockID returns all customers pinned to the given
	// test clock, with PII fields decrypted. Used by the testclock
	// domain via a narrow CustomerReader interface so that domain
	// never reads the customers table directly — keeps decryption
	// centralised on the customer-package read path.
	ListByTestClockID(ctx context.Context, tenantID, clockID string) ([]domain.Customer, error)
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

	// SetCostDashboardToken writes (or rotates) the public cost-dashboard
	// token on a customer row. Old tokens are discarded immediately —
	// the public route validates against this single column, so the
	// previous URL stops working as soon as the UPDATE commits.
	SetCostDashboardToken(ctx context.Context, tenantID, customerID, token string) error

	// GetByCostDashboardToken resolves the customer behind a public
	// cost-dashboard token. RLS-bypass query (the token IS the
	// credential — no tenant context yet); the returned customer's
	// tenant_id scopes everything that follows.
	GetByCostDashboardToken(ctx context.Context, token string) (domain.Customer, error)
}

type ListFilter struct {
	TenantID   string
	Status     string
	ExternalID string
	Limit      int
	Offset     int
	Sort       string // closed allow-list (validated in store); empty defaults to created_at
	SortDir    string // "asc" or "desc"; empty defaults to desc
}
