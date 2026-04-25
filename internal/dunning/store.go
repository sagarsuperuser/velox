package dunning

import (
	"context"
	"database/sql"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
)

type Store interface {
	// Policy
	GetPolicy(ctx context.Context, tenantID string) (domain.DunningPolicy, error)
	UpsertPolicy(ctx context.Context, tenantID string, policy domain.DunningPolicy) (domain.DunningPolicy, error)
	UpsertPolicyTx(ctx context.Context, tx *sql.Tx, tenantID string, policy domain.DunningPolicy) (domain.DunningPolicy, error)

	// Runs
	CreateRun(ctx context.Context, tenantID string, run domain.InvoiceDunningRun) (domain.InvoiceDunningRun, error)
	GetRun(ctx context.Context, tenantID, id string) (domain.InvoiceDunningRun, error)
	GetActiveRunByInvoice(ctx context.Context, tenantID, invoiceID string) (domain.InvoiceDunningRun, error)
	ListRuns(ctx context.Context, filter RunListFilter) ([]domain.InvoiceDunningRun, int, error)
	UpdateRun(ctx context.Context, tenantID string, run domain.InvoiceDunningRun) (domain.InvoiceDunningRun, error)
	ListDueRuns(ctx context.Context, tenantID string, dueBefore time.Time, limit int) ([]domain.InvoiceDunningRun, error)

	// Events
	CreateEvent(ctx context.Context, tenantID string, event domain.InvoiceDunningEvent) (domain.InvoiceDunningEvent, error)
	ListEvents(ctx context.Context, tenantID, runID string) ([]domain.InvoiceDunningEvent, error)

	// Customer dunning overrides
	GetCustomerOverride(ctx context.Context, tenantID, customerID string) (domain.CustomerDunningOverride, error)
	UpsertCustomerOverride(ctx context.Context, tenantID string, override domain.CustomerDunningOverride) (domain.CustomerDunningOverride, error)
	DeleteCustomerOverride(ctx context.Context, tenantID, customerID string) error
}

type RunListFilter struct {
	TenantID   string
	InvoiceID  string
	CustomerID string
	State      string
	ActiveOnly bool
	Limit      int
	Offset     int
}
