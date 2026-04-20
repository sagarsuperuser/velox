package credit

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
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

	// SourceSubscriptionID + SourceSubscriptionItemID + SourcePlanChangedAt +
	// SourceChangeType, when all set, mark this grant as a proration credit
	// for a specific item mutation (plan downgrade, qty reduction, remove).
	// The store enforces uniqueness on the full tuple so retries return
	// ErrAlreadyExists instead of duplicating the credit — see migration 0027
	// for the unique partial index.
	SourceSubscriptionID     string                `json:"source_subscription_id,omitempty"`
	SourceSubscriptionItemID string                `json:"source_subscription_item_id,omitempty"`
	SourcePlanChangedAt      *time.Time            `json:"source_plan_changed_at,omitempty"`
	SourceChangeType         domain.ItemChangeType `json:"source_change_type,omitempty"`
}

func (s *Service) Grant(ctx context.Context, tenantID string, input GrantInput) (domain.CreditLedgerEntry, error) {
	if input.CustomerID == "" {
		return domain.CreditLedgerEntry{}, errs.Required("customer_id")
	}
	if input.AmountCents <= 0 {
		return domain.CreditLedgerEntry{}, errs.Invalid("amount_cents", "must be greater than 0")
	}
	if input.AmountCents > 100_000_000 { // $1M cap
		return domain.CreditLedgerEntry{}, errs.Invalid("amount_cents", "cannot exceed 1,000,000")
	}
	desc := strings.TrimSpace(input.Description)
	if desc == "" {
		return domain.CreditLedgerEntry{}, errs.Required("description")
	}
	if len(desc) > 500 {
		return domain.CreditLedgerEntry{}, errs.Invalid("description", "must be at most 500 characters")
	}

	return s.store.AppendEntry(ctx, tenantID, domain.CreditLedgerEntry{
		CustomerID:               input.CustomerID,
		EntryType:                domain.CreditGrant,
		AmountCents:              input.AmountCents,
		Description:              desc,
		InvoiceID:                input.InvoiceID,
		ExpiresAt:                input.ExpiresAt,
		SourceSubscriptionID:     input.SourceSubscriptionID,
		SourceSubscriptionItemID: input.SourceSubscriptionItemID,
		SourcePlanChangedAt:      input.SourcePlanChangedAt,
		SourceChangeType:         input.SourceChangeType,
	})
}

// GetByProrationSource exposes the store-level source lookup. Used by the
// subscription proration path to complete an idempotent retry after
// AppendEntry returns ErrAlreadyExists.
func (s *Service) GetByProrationSource(ctx context.Context, tenantID, subscriptionID, subscriptionItemID string, changeType domain.ItemChangeType, changeAt time.Time) (domain.CreditLedgerEntry, error) {
	return s.store.GetByProrationSource(ctx, tenantID, subscriptionID, subscriptionItemID, changeType, changeAt)
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
	var expiryErrs []error
	for _, g := range grants {
		_, err := s.store.AppendEntry(ctx, g.TenantID, domain.CreditLedgerEntry{
			CustomerID:  g.CustomerID,
			EntryType:   domain.CreditExpiry,
			AmountCents: -g.AmountCents,
			Description: fmt.Sprintf("Expired grant %s", g.ID),
		})
		if err != nil {
			expiryErrs = append(expiryErrs, fmt.Errorf("expire grant %s: %w", g.ID, err))
			continue
		}
		expired++
	}
	return expired, expiryErrs
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
		return domain.CreditLedgerEntry{}, errs.Required("customer_id")
	}
	if input.AmountCents == 0 {
		return domain.CreditLedgerEntry{}, errs.Invalid("amount_cents", "cannot be zero")
	}
	desc := strings.TrimSpace(input.Description)
	if desc == "" {
		return domain.CreditLedgerEntry{}, errs.Required("description")
	}

	return s.store.AdjustAtomic(ctx, tenantID, input.CustomerID, desc, input.AmountCents)
}
