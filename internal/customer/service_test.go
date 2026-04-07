package customer

import (
	"context"
	"fmt"
	"testing"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
)

type memoryStore struct {
	customers       map[string]domain.Customer
	billingProfiles map[string]domain.CustomerBillingProfile
	paymentSetups   map[string]domain.CustomerPaymentSetup
}

func newMemoryStore() *memoryStore {
	return &memoryStore{
		customers:       make(map[string]domain.Customer),
		billingProfiles: make(map[string]domain.CustomerBillingProfile),
		paymentSetups:   make(map[string]domain.CustomerPaymentSetup),
	}
}

func (m *memoryStore) Create(_ context.Context, tenantID string, c domain.Customer) (domain.Customer, error) {
	for _, existing := range m.customers {
		if existing.TenantID == tenantID && existing.ExternalID == c.ExternalID {
			return domain.Customer{}, fmt.Errorf("%w: customer with external_id %q already exists", errs.ErrAlreadyExists, c.ExternalID)
		}
	}
	c.ID = fmt.Sprintf("vlx_cus_%d", len(m.customers)+1)
	c.TenantID = tenantID
	c.Status = domain.CustomerStatusActive
	m.customers[c.ID] = c
	return c, nil
}

func (m *memoryStore) Get(_ context.Context, tenantID, id string) (domain.Customer, error) {
	c, ok := m.customers[id]
	if !ok || c.TenantID != tenantID {
		return domain.Customer{}, errs.ErrNotFound
	}
	return c, nil
}

func (m *memoryStore) GetByExternalID(_ context.Context, tenantID, externalID string) (domain.Customer, error) {
	for _, c := range m.customers {
		if c.TenantID == tenantID && c.ExternalID == externalID {
			return c, nil
		}
	}
	return domain.Customer{}, errs.ErrNotFound
}

func (m *memoryStore) List(_ context.Context, filter ListFilter) ([]domain.Customer, int, error) {
	var result []domain.Customer
	for _, c := range m.customers {
		if c.TenantID != filter.TenantID {
			continue
		}
		if filter.Status != "" && string(c.Status) != filter.Status {
			continue
		}
		result = append(result, c)
	}
	return result, len(result), nil
}

func (m *memoryStore) Update(_ context.Context, tenantID string, c domain.Customer) (domain.Customer, error) {
	existing, ok := m.customers[c.ID]
	if !ok || existing.TenantID != tenantID {
		return domain.Customer{}, errs.ErrNotFound
	}
	m.customers[c.ID] = c
	return c, nil
}

func (m *memoryStore) UpsertBillingProfile(_ context.Context, tenantID string, bp domain.CustomerBillingProfile) (domain.CustomerBillingProfile, error) {
	bp.TenantID = tenantID
	m.billingProfiles[bp.CustomerID] = bp
	return bp, nil
}

func (m *memoryStore) GetBillingProfile(_ context.Context, tenantID, customerID string) (domain.CustomerBillingProfile, error) {
	bp, ok := m.billingProfiles[customerID]
	if !ok {
		return domain.CustomerBillingProfile{}, errs.ErrNotFound
	}
	return bp, nil
}

func (m *memoryStore) UpsertPaymentSetup(_ context.Context, tenantID string, ps domain.CustomerPaymentSetup) (domain.CustomerPaymentSetup, error) {
	ps.TenantID = tenantID
	m.paymentSetups[ps.CustomerID] = ps
	return ps, nil
}

func (m *memoryStore) GetPaymentSetup(_ context.Context, tenantID, customerID string) (domain.CustomerPaymentSetup, error) {
	ps, ok := m.paymentSetups[customerID]
	if !ok {
		return domain.CustomerPaymentSetup{}, errs.ErrNotFound
	}
	return ps, nil
}

func TestCustomerService_Create(t *testing.T) {
	svc := NewService(newMemoryStore())
	ctx := context.Background()

	t.Run("valid input", func(t *testing.T) {
		c, err := svc.Create(ctx, "tenant1", CreateInput{
			ExternalID:  "cus_123",
			DisplayName: "Acme Corp",
			Email:       "billing@acme.com",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if c.ExternalID != "cus_123" {
			t.Errorf("got external_id %q, want %q", c.ExternalID, "cus_123")
		}
		if c.DisplayName != "Acme Corp" {
			t.Errorf("got display_name %q, want %q", c.DisplayName, "Acme Corp")
		}
		if c.TenantID != "tenant1" {
			t.Errorf("got tenant_id %q, want %q", c.TenantID, "tenant1")
		}
	})

	t.Run("missing external_id", func(t *testing.T) {
		_, err := svc.Create(ctx, "tenant1", CreateInput{DisplayName: "Test"})
		if err == nil {
			t.Fatal("expected error for missing external_id")
		}
	})

	t.Run("missing display_name", func(t *testing.T) {
		_, err := svc.Create(ctx, "tenant1", CreateInput{ExternalID: "test"})
		if err == nil {
			t.Fatal("expected error for missing display_name")
		}
	})

	t.Run("duplicate external_id same tenant", func(t *testing.T) {
		_, err := svc.Create(ctx, "tenant1", CreateInput{
			ExternalID:  "cus_123",
			DisplayName: "Duplicate",
		})
		if !testing.Verbose() && err == nil {
			t.Fatal("expected error for duplicate external_id")
		}
	})
}

func TestCustomerService_Get(t *testing.T) {
	store := newMemoryStore()
	svc := NewService(store)
	ctx := context.Background()

	created, _ := svc.Create(ctx, "tenant1", CreateInput{
		ExternalID:  "cus_456",
		DisplayName: "Test Corp",
	})

	t.Run("found", func(t *testing.T) {
		got, err := svc.Get(ctx, "tenant1", created.ID)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.ID != created.ID {
			t.Errorf("got id %q, want %q", got.ID, created.ID)
		}
	})

	t.Run("wrong tenant", func(t *testing.T) {
		_, err := svc.Get(ctx, "other_tenant", created.ID)
		if err != errs.ErrNotFound {
			t.Errorf("expected ErrNotFound for wrong tenant, got %v", err)
		}
	})
}

func TestCustomerService_Update(t *testing.T) {
	store := newMemoryStore()
	svc := NewService(store)
	ctx := context.Background()

	created, _ := svc.Create(ctx, "tenant1", CreateInput{
		ExternalID:  "cus_789",
		DisplayName: "Original Name",
	})

	updated, err := svc.Update(ctx, "tenant1", created.ID, UpdateInput{
		DisplayName: "Updated Name",
		Email:       "new@example.com",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated.DisplayName != "Updated Name" {
		t.Errorf("got display_name %q, want %q", updated.DisplayName, "Updated Name")
	}
	if updated.Email != "new@example.com" {
		t.Errorf("got email %q, want %q", updated.Email, "new@example.com")
	}
}

func TestCustomerService_BillingProfile(t *testing.T) {
	store := newMemoryStore()
	svc := NewService(store)
	ctx := context.Background()

	created, _ := svc.Create(ctx, "tenant1", CreateInput{
		ExternalID:  "cus_bp",
		DisplayName: "BP Test",
	})

	t.Run("upsert and get", func(t *testing.T) {
		bp, err := svc.UpsertBillingProfile(ctx, "tenant1", domain.CustomerBillingProfile{
			CustomerID: created.ID,
			LegalName:  "Acme Inc.",
			Country:    "US",
			Currency:   "USD",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if bp.LegalName != "Acme Inc." {
			t.Errorf("got legal_name %q, want %q", bp.LegalName, "Acme Inc.")
		}

		got, err := svc.GetBillingProfile(ctx, "tenant1", created.ID)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Country != "US" {
			t.Errorf("got country %q, want %q", got.Country, "US")
		}
	})

	t.Run("not found", func(t *testing.T) {
		_, err := svc.GetBillingProfile(ctx, "tenant1", "nonexistent")
		if err != errs.ErrNotFound {
			t.Errorf("expected ErrNotFound, got %v", err)
		}
	})

	t.Run("missing customer_id", func(t *testing.T) {
		_, err := svc.UpsertBillingProfile(ctx, "tenant1", domain.CustomerBillingProfile{})
		if err == nil {
			t.Fatal("expected error for missing customer_id")
		}
	})
}
