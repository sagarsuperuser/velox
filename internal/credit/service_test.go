package credit

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
)

type memStore struct {
	entries []domain.CreditLedgerEntry
}

func newMemStore() *memStore {
	return &memStore{}
}

func (m *memStore) AppendEntry(_ context.Context, tenantID string, entry domain.CreditLedgerEntry) (domain.CreditLedgerEntry, error) {
	// Emulate the proration dedup partial unique index. Without this, tests
	// exercising retry-after-partial-failure paths silently double-insert
	// rows the real DB would have rejected.
	if entry.SourceSubscriptionID != "" && entry.SourcePlanChangedAt != nil {
		for _, e := range m.entries {
			if e.TenantID == tenantID && e.SourceSubscriptionID == entry.SourceSubscriptionID &&
				e.SourcePlanChangedAt != nil && e.SourcePlanChangedAt.Equal(*entry.SourcePlanChangedAt) {
				return domain.CreditLedgerEntry{}, errs.ErrAlreadyExists
			}
		}
	}

	// Compute balance
	var balance int64
	for _, e := range m.entries {
		if e.CustomerID == entry.CustomerID {
			balance += e.AmountCents
		}
	}
	entry.BalanceAfter = balance + entry.AmountCents
	entry.ID = fmt.Sprintf("vlx_ccl_%d", len(m.entries)+1)
	entry.TenantID = tenantID
	entry.CreatedAt = time.Now().UTC()
	m.entries = append(m.entries, entry)
	return entry, nil
}

func (m *memStore) GetByProrationSource(_ context.Context, tenantID, subscriptionID string, planChangedAt time.Time) (domain.CreditLedgerEntry, error) {
	for _, e := range m.entries {
		if e.TenantID == tenantID && e.SourceSubscriptionID == subscriptionID &&
			e.SourcePlanChangedAt != nil && e.SourcePlanChangedAt.Equal(planChangedAt) {
			return e, nil
		}
	}
	return domain.CreditLedgerEntry{}, errs.ErrNotFound
}

func (m *memStore) GetBalance(_ context.Context, _, customerID string) (domain.CreditBalance, error) {
	var b domain.CreditBalance
	b.CustomerID = customerID
	for _, e := range m.entries {
		if e.CustomerID != customerID {
			continue
		}
		b.BalanceCents += e.AmountCents
		switch e.EntryType {
		case domain.CreditGrant:
			b.TotalGranted += e.AmountCents
		case domain.CreditUsage:
			b.TotalUsed += -e.AmountCents
		case domain.CreditExpiry:
			b.TotalExpired += -e.AmountCents
		}
	}
	return b, nil
}

func (m *memStore) ListBalances(_ context.Context, _ string) ([]domain.CreditBalance, error) {
	byCustomer := map[string]*domain.CreditBalance{}
	for _, e := range m.entries {
		b, ok := byCustomer[e.CustomerID]
		if !ok {
			b = &domain.CreditBalance{CustomerID: e.CustomerID}
			byCustomer[e.CustomerID] = b
		}
		b.BalanceCents += e.AmountCents
		switch e.EntryType {
		case domain.CreditGrant:
			b.TotalGranted += e.AmountCents
		case domain.CreditUsage:
			b.TotalUsed += -e.AmountCents
		}
	}
	var result []domain.CreditBalance
	for _, b := range byCustomer {
		result = append(result, *b)
	}
	return result, nil
}

func (m *memStore) ListExpiredGrants(_ context.Context) ([]domain.CreditLedgerEntry, error) {
	var result []domain.CreditLedgerEntry
	for _, e := range m.entries {
		if e.EntryType != domain.CreditGrant || e.ExpiresAt == nil || !e.ExpiresAt.Before(time.Now()) {
			continue
		}
		// Check no expiry entry already exists for this grant
		expired := false
		for _, e2 := range m.entries {
			if e2.EntryType == domain.CreditExpiry && e2.Description == fmt.Sprintf("Expired grant %s", e.ID) {
				expired = true
				break
			}
		}
		if !expired {
			result = append(result, e)
		}
	}
	return result, nil
}

func (m *memStore) AdjustAtomic(ctx context.Context, tenantID, customerID, description string, amountCents int64) (domain.CreditLedgerEntry, error) {
	bal, err := m.GetBalance(ctx, tenantID, customerID)
	if err != nil {
		return domain.CreditLedgerEntry{}, err
	}
	if amountCents < 0 && bal.BalanceCents+amountCents < 0 {
		return domain.CreditLedgerEntry{}, fmt.Errorf("insufficient balance: available %.2f, deduction %.2f",
			float64(bal.BalanceCents)/100, float64(-amountCents)/100)
	}
	return m.AppendEntry(ctx, tenantID, domain.CreditLedgerEntry{
		CustomerID:  customerID,
		EntryType:   domain.CreditAdjustment,
		AmountCents: amountCents,
		Description: description,
	})
}

func (m *memStore) ApplyToInvoiceAtomic(ctx context.Context, tenantID, customerID, invoiceID, invoiceDesc string, invoiceAmountCents int64) (int64, error) {
	if invoiceAmountCents <= 0 {
		return 0, nil
	}
	bal, err := m.GetBalance(ctx, tenantID, customerID)
	if err != nil {
		return 0, err
	}
	if bal.BalanceCents <= 0 {
		return 0, nil
	}
	deduct := min(bal.BalanceCents, invoiceAmountCents)
	if _, err := m.AppendEntry(ctx, tenantID, domain.CreditLedgerEntry{
		CustomerID:  customerID,
		EntryType:   domain.CreditUsage,
		AmountCents: -deduct,
		Description: invoiceDesc,
		InvoiceID:   invoiceID,
	}); err != nil {
		return 0, err
	}
	return deduct, nil
}

func (m *memStore) ListEntries(_ context.Context, filter ListFilter) ([]domain.CreditLedgerEntry, error) {
	var result []domain.CreditLedgerEntry
	for _, e := range m.entries {
		if e.CustomerID != filter.CustomerID {
			continue
		}
		if filter.EntryType != "" && string(e.EntryType) != filter.EntryType {
			continue
		}
		result = append(result, e)
	}
	return result, nil
}

func TestGrant(t *testing.T) {
	svc := NewService(newMemStore())
	ctx := context.Background()

	t.Run("valid grant", func(t *testing.T) {
		entry, err := svc.Grant(ctx, "t1", GrantInput{
			CustomerID:  "cus_1",
			AmountCents: 50000,
			Description: "$500 promotional credit",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if entry.EntryType != domain.CreditGrant {
			t.Errorf("type: got %q, want grant", entry.EntryType)
		}
		if entry.AmountCents != 50000 {
			t.Errorf("amount: got %d, want 50000", entry.AmountCents)
		}
		if entry.BalanceAfter != 50000 {
			t.Errorf("balance_after: got %d, want 50000", entry.BalanceAfter)
		}
	})

	t.Run("missing customer", func(t *testing.T) {
		_, err := svc.Grant(ctx, "t1", GrantInput{AmountCents: 100})
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("zero amount", func(t *testing.T) {
		_, err := svc.Grant(ctx, "t1", GrantInput{CustomerID: "c", AmountCents: 0})
		if err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestApplyToInvoice(t *testing.T) {
	svc := NewService(newMemStore())
	ctx := context.Background()

	// Grant $500
	_, _ = svc.Grant(ctx, "t1", GrantInput{CustomerID: "cus_1", AmountCents: 50000, Description: "Test grant"})

	t.Run("partial application", func(t *testing.T) {
		deducted, err := svc.ApplyToInvoice(ctx, "t1", "cus_1", "inv_1", 19900)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if deducted != 19900 {
			t.Errorf("deducted: got %d, want 19900", deducted)
		}

		bal, _ := svc.GetBalance(ctx, "t1", "cus_1")
		if bal.BalanceCents != 30100 {
			t.Errorf("remaining balance: got %d, want 30100", bal.BalanceCents)
		}
	})

	t.Run("exceeds balance", func(t *testing.T) {
		// Balance is now 30100, try to apply 50000
		deducted, err := svc.ApplyToInvoice(ctx, "t1", "cus_1", "inv_2", 50000)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if deducted != 30100 {
			t.Errorf("deducted: got %d, want 30100 (uses remaining balance)", deducted)
		}

		bal, _ := svc.GetBalance(ctx, "t1", "cus_1")
		if bal.BalanceCents != 0 {
			t.Errorf("remaining balance: got %d, want 0", bal.BalanceCents)
		}
	})

	t.Run("no balance left", func(t *testing.T) {
		deducted, err := svc.ApplyToInvoice(ctx, "t1", "cus_1", "inv_3", 10000)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if deducted != 0 {
			t.Errorf("deducted: got %d, want 0", deducted)
		}
	})
}

func TestAdjust(t *testing.T) {
	svc := NewService(newMemStore())
	ctx := context.Background()

	_, _ = svc.Grant(ctx, "t1", GrantInput{CustomerID: "cus_1", AmountCents: 10000, Description: "Test grant"})

	t.Run("positive adjustment", func(t *testing.T) {
		entry, err := svc.Adjust(ctx, "t1", AdjustInput{
			CustomerID: "cus_1", AmountCents: 5000, Description: "Goodwill credit",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if entry.BalanceAfter != 15000 {
			t.Errorf("balance: got %d, want 15000", entry.BalanceAfter)
		}
	})

	t.Run("negative adjustment", func(t *testing.T) {
		entry, err := svc.Adjust(ctx, "t1", AdjustInput{
			CustomerID: "cus_1", AmountCents: -3000, Description: "Correction",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if entry.BalanceAfter != 12000 {
			t.Errorf("balance: got %d, want 12000", entry.BalanceAfter)
		}
	})

	t.Run("missing description", func(t *testing.T) {
		_, err := svc.Adjust(ctx, "t1", AdjustInput{CustomerID: "c", AmountCents: 100})
		if err == nil {
			t.Fatal("expected error for missing description")
		}
	})
}

func TestGetBalance(t *testing.T) {
	svc := NewService(newMemStore())
	ctx := context.Background()

	_, _ = svc.Grant(ctx, "t1", GrantInput{CustomerID: "cus_1", AmountCents: 50000, Description: "Test grant"})
	_, _ = svc.ApplyToInvoice(ctx, "t1", "cus_1", "inv_1", 20000)

	bal, err := svc.GetBalance(ctx, "t1", "cus_1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bal.BalanceCents != 30000 {
		t.Errorf("balance: got %d, want 30000", bal.BalanceCents)
	}
	if bal.TotalGranted != 50000 {
		t.Errorf("granted: got %d, want 50000", bal.TotalGranted)
	}
	if bal.TotalUsed != 20000 {
		t.Errorf("used: got %d, want 20000", bal.TotalUsed)
	}
}
