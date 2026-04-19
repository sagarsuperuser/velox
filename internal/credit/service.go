package credit

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
)

type Service struct {
	store Store
}

func NewService(store Store) *Service {
	return &Service{store: store}
}

type GrantInput struct {
	CustomerID  string     `json:"customer_id"`
	AmountCents int64      `json:"amount_cents"`
	Description string     `json:"description"`
	InvoiceID   string     `json:"invoice_id,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
}

func (s *Service) Grant(ctx context.Context, tenantID string, input GrantInput) (domain.CreditLedgerEntry, error) {
	if input.CustomerID == "" {
		return domain.CreditLedgerEntry{}, fmt.Errorf("customer_id is required")
	}
	if input.AmountCents <= 0 {
		return domain.CreditLedgerEntry{}, fmt.Errorf("amount must be greater than 0")
	}
	if input.AmountCents > 100_000_000 { // $1M cap
		return domain.CreditLedgerEntry{}, fmt.Errorf("amount cannot exceed 1,000,000")
	}
	desc := strings.TrimSpace(input.Description)
	if desc == "" {
		return domain.CreditLedgerEntry{}, fmt.Errorf("description is required")
	}
	if len(desc) > 500 {
		return domain.CreditLedgerEntry{}, fmt.Errorf("description must be at most 500 characters")
	}

	return s.store.AppendEntry(ctx, tenantID, domain.CreditLedgerEntry{
		CustomerID:  input.CustomerID,
		EntryType:   domain.CreditGrant,
		AmountCents: input.AmountCents,
		Description: desc,
		InvoiceID:   input.InvoiceID,
		ExpiresAt:   input.ExpiresAt,
	})
}

// ApplyToInvoice deducts credits from a customer's balance AND reduces the
// invoice's amount_due_cents in a single atomic transaction. Either both
// happen or neither does — there is no window where the ledger is debited
// but the invoice still shows the pre-credit amount due (which would cause
// double-billing when the PaymentIntent charges for the full original total).
//
// Returns the amount deducted. If balance is 0 or invoice amount is 0,
// returns 0 without any writes.
func (s *Service) ApplyToInvoice(ctx context.Context, tenantID, customerID, invoiceID string, invoiceAmountCents int64, invoiceNumber ...string) (int64, error) {
	desc := fmt.Sprintf("Applied to invoice %s", invoiceID)
	if len(invoiceNumber) > 0 && invoiceNumber[0] != "" {
		desc = fmt.Sprintf("Applied to invoice %s", invoiceNumber[0])
	}
	return s.store.ApplyToInvoiceAtomic(ctx, tenantID, customerID, invoiceID, desc, invoiceAmountCents)
}

func (s *Service) ReverseForInvoice(ctx context.Context, tenantID, customerID, invoiceID, invoiceNumber string) (int64, error) {
	entries, err := s.store.ListEntries(ctx, ListFilter{
		TenantID:   tenantID,
		CustomerID: customerID,
		InvoiceID:  invoiceID,
		EntryType:  string(domain.CreditUsage),
		Limit:      100,
	})
	if err != nil {
		return 0, err
	}

	// Sum all usage entries for this invoice (they're negative)
	var totalUsed int64
	for _, e := range entries {
		totalUsed += -e.AmountCents // Convert negative to positive
	}

	if totalUsed <= 0 {
		return 0, nil // No credits were applied to this invoice
	}

	desc := fmt.Sprintf("Reversed — invoice %s voided", invoiceNumber)
	if invoiceNumber == "" {
		desc = fmt.Sprintf("Reversed — invoice %s voided", invoiceID)
	}

	_, err = s.store.AppendEntry(ctx, tenantID, domain.CreditLedgerEntry{
		CustomerID:  customerID,
		EntryType:   domain.CreditGrant,
		AmountCents: totalUsed,
		Description: desc,
		InvoiceID:   invoiceID,
	})
	if err != nil {
		return 0, err
	}

	return totalUsed, nil
}

// ExpireCredits finds unexpired grant entries past their expiry date and creates
// negative (expiry) entries to zero them out. Returns the count of expired grants
// and any errors encountered during processing.
func (s *Service) ExpireCredits(ctx context.Context) (int, []error) {
	grants, err := s.store.ListExpiredGrants(ctx)
	if err != nil {
		return 0, []error{fmt.Errorf("list expired grants: %w", err)}
	}

	var expired int
	var errs []error
	for _, g := range grants {
		_, err := s.store.AppendEntry(ctx, g.TenantID, domain.CreditLedgerEntry{
			CustomerID:  g.CustomerID,
			EntryType:   domain.CreditExpiry,
			AmountCents: -g.AmountCents,
			Description: fmt.Sprintf("Expired grant %s", g.ID),
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("expire grant %s: %w", g.ID, err))
			continue
		}
		expired++
	}
	return expired, errs
}

func (s *Service) GetBalance(ctx context.Context, tenantID, customerID string) (domain.CreditBalance, error) {
	return s.store.GetBalance(ctx, tenantID, customerID)
}

func (s *Service) ListBalances(ctx context.Context, tenantID string) ([]domain.CreditBalance, error) {
	return s.store.ListBalances(ctx, tenantID)
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

	// Prevent negative balance on deductions
	if input.AmountCents < 0 {
		bal, err := s.store.GetBalance(ctx, tenantID, input.CustomerID)
		if err != nil {
			return domain.CreditLedgerEntry{}, fmt.Errorf("get balance: %w", err)
		}
		if bal.BalanceCents+input.AmountCents < 0 {
			return domain.CreditLedgerEntry{}, fmt.Errorf("insufficient balance: available %.2f, deduction %.2f",
				float64(bal.BalanceCents)/100, float64(-input.AmountCents)/100)
		}
	}

	return s.store.AppendEntry(ctx, tenantID, domain.CreditLedgerEntry{
		CustomerID:  input.CustomerID,
		EntryType:   domain.CreditAdjustment,
		AmountCents: input.AmountCents,
		Description: desc,
	})
}
