package credit

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

type GrantInput struct {
	CustomerID  string `json:"customer_id"`
	AmountCents int64  `json:"amount_cents"`
	Description string `json:"description"`
}

func (s *Service) Grant(ctx context.Context, tenantID string, input GrantInput) (domain.CreditLedgerEntry, error) {
	if input.CustomerID == "" {
		return domain.CreditLedgerEntry{}, fmt.Errorf("customer_id is required")
	}
	if input.AmountCents <= 0 {
		return domain.CreditLedgerEntry{}, fmt.Errorf("amount_cents must be positive")
	}
	desc := strings.TrimSpace(input.Description)
	if desc == "" {
		desc = "Credit grant"
	}

	return s.store.AppendEntry(ctx, tenantID, domain.CreditLedgerEntry{
		CustomerID:  input.CustomerID,
		EntryType:   domain.CreditGrant,
		AmountCents: input.AmountCents,
		Description: desc,
	})
}

// ApplyToInvoice deducts credits from a customer's balance and returns the
// amount deducted. If balance is less than invoiceAmount, uses what's available.
func (s *Service) ApplyToInvoice(ctx context.Context, tenantID, customerID, invoiceID string, invoiceAmountCents int64) (int64, error) {
	bal, err := s.store.GetBalance(ctx, tenantID, customerID)
	if err != nil {
		return 0, err
	}

	if bal.BalanceCents <= 0 {
		return 0, nil // No credits to apply
	}

	deduct := bal.BalanceCents
	if deduct > invoiceAmountCents {
		deduct = invoiceAmountCents
	}

	_, err = s.store.AppendEntry(ctx, tenantID, domain.CreditLedgerEntry{
		CustomerID:  customerID,
		EntryType:   domain.CreditUsage,
		AmountCents: -deduct, // Negative = deduction
		Description: fmt.Sprintf("Applied to invoice %s", invoiceID),
		InvoiceID:   invoiceID,
	})
	if err != nil {
		return 0, err
	}

	return deduct, nil
}

func (s *Service) GetBalance(ctx context.Context, tenantID, customerID string) (domain.CreditBalance, error) {
	return s.store.GetBalance(ctx, tenantID, customerID)
}

func (s *Service) ListEntries(ctx context.Context, filter ListFilter) ([]domain.CreditLedgerEntry, error) {
	return s.store.ListEntries(ctx, filter)
}

type AdjustInput struct {
	CustomerID  string `json:"customer_id"`
	AmountCents int64  `json:"amount_cents"` // Positive or negative
	Description string `json:"description"`
}

func (s *Service) Adjust(ctx context.Context, tenantID string, input AdjustInput) (domain.CreditLedgerEntry, error) {
	if input.CustomerID == "" {
		return domain.CreditLedgerEntry{}, fmt.Errorf("customer_id is required")
	}
	if input.AmountCents == 0 {
		return domain.CreditLedgerEntry{}, fmt.Errorf("amount_cents cannot be zero")
	}
	desc := strings.TrimSpace(input.Description)
	if desc == "" {
		return domain.CreditLedgerEntry{}, fmt.Errorf("description is required for adjustments")
	}

	return s.store.AppendEntry(ctx, tenantID, domain.CreditLedgerEntry{
		CustomerID:  input.CustomerID,
		EntryType:   domain.CreditAdjustment,
		AmountCents: input.AmountCents,
		Description: desc,
	})
}
