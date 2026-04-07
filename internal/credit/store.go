package credit

import (
	"context"

	"github.com/sagarsuperuser/velox/internal/domain"
)

type Store interface {
	AppendEntry(ctx context.Context, tenantID string, entry domain.CreditLedgerEntry) (domain.CreditLedgerEntry, error)
	GetBalance(ctx context.Context, tenantID, customerID string) (domain.CreditBalance, error)
	ListEntries(ctx context.Context, filter ListFilter) ([]domain.CreditLedgerEntry, error)
}

type ListFilter struct {
	TenantID   string
	CustomerID string
	EntryType  string
	Limit      int
	Offset     int
}
