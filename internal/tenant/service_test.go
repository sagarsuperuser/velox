package tenant

import (
	"context"
	"testing"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
)

// memoryStore is an in-memory Store implementation for unit tests.
type memoryStore struct {
	tenants map[string]domain.Tenant
}

func newMemoryStore() *memoryStore {
	return &memoryStore{tenants: make(map[string]domain.Tenant)}
}

func (m *memoryStore) Create(_ context.Context, t domain.Tenant) (domain.Tenant, error) {
	t.ID = "vlx_ten_test"
	t.Status = domain.TenantStatusActive
	m.tenants[t.ID] = t
	return t, nil
}

func (m *memoryStore) Get(_ context.Context, id string) (domain.Tenant, error) {
	t, ok := m.tenants[id]
	if !ok {
		return domain.Tenant{}, errs.ErrNotFound
	}
	return t, nil
}

func (m *memoryStore) List(_ context.Context, _ ListFilter) ([]domain.Tenant, error) {
	var result []domain.Tenant
	for _, t := range m.tenants {
		result = append(result, t)
	}
	return result, nil
}

func (m *memoryStore) UpdateStatus(_ context.Context, id string, status domain.TenantStatus) (domain.Tenant, error) {
	t, ok := m.tenants[id]
	if !ok {
		return domain.Tenant{}, errs.ErrNotFound
	}
	t.Status = status
	m.tenants[id] = t
	return t, nil
}

func TestService_Create(t *testing.T) {
	svc := NewService(newMemoryStore())
	ctx := context.Background()

	t.Run("valid name", func(t *testing.T) {
		tenant, err := svc.Create(ctx, CreateInput{Name: "Acme Corp"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if tenant.Name != "Acme Corp" {
			t.Errorf("got name %q, want %q", tenant.Name, "Acme Corp")
		}
		if tenant.Status != domain.TenantStatusActive {
			t.Errorf("got status %q, want %q", tenant.Status, domain.TenantStatusActive)
		}
	})

	t.Run("empty name", func(t *testing.T) {
		_, err := svc.Create(ctx, CreateInput{Name: ""})
		if err == nil {
			t.Fatal("expected error for empty name")
		}
	})

	t.Run("whitespace name", func(t *testing.T) {
		_, err := svc.Create(ctx, CreateInput{Name: "   "})
		if err == nil {
			t.Fatal("expected error for whitespace name")
		}
	})
}

func TestService_Get(t *testing.T) {
	store := newMemoryStore()
	svc := NewService(store)
	ctx := context.Background()

	created, _ := svc.Create(ctx, CreateInput{Name: "Test"})

	t.Run("found", func(t *testing.T) {
		got, err := svc.Get(ctx, created.ID)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.ID != created.ID {
			t.Errorf("got id %q, want %q", got.ID, created.ID)
		}
	})

	t.Run("not found", func(t *testing.T) {
		_, err := svc.Get(ctx, "nonexistent")
		if err != errs.ErrNotFound {
			t.Errorf("got error %v, want ErrNotFound", err)
		}
	})

	t.Run("empty id", func(t *testing.T) {
		_, err := svc.Get(ctx, "")
		if err == nil {
			t.Fatal("expected error for empty id")
		}
	})
}

func TestService_UpdateStatus(t *testing.T) {
	store := newMemoryStore()
	svc := NewService(store)
	ctx := context.Background()

	created, _ := svc.Create(ctx, CreateInput{Name: "Test"})

	updated, err := svc.UpdateStatus(ctx, created.ID, domain.TenantStatusSuspended)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated.Status != domain.TenantStatusSuspended {
		t.Errorf("got status %q, want %q", updated.Status, domain.TenantStatusSuspended)
	}
}
