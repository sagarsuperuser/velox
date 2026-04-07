package tenant_test

import (
	"context"
	"testing"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/tenant"
	"github.com/sagarsuperuser/velox/internal/testutil"
)

func TestPostgresStore_CreateAndGet(t *testing.T) {
	db := testutil.SetupTestDB(t)
	store := tenant.NewPostgresStore(db)
	ctx := context.Background()

	created, err := store.Create(ctx, domain.Tenant{Name: "Acme Corp"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID == "" {
		t.Fatal("ID should be generated")
	}
	if created.Name != "Acme Corp" {
		t.Errorf("name: got %q, want Acme Corp", created.Name)
	}
	if created.Status != domain.TenantStatusActive {
		t.Errorf("status: got %q, want active", created.Status)
	}

	got, err := store.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != created.ID {
		t.Errorf("get ID mismatch: got %q, want %q", got.ID, created.ID)
	}
	if got.Name != "Acme Corp" {
		t.Errorf("get name: got %q", got.Name)
	}
}

func TestPostgresStore_GetNotFound(t *testing.T) {
	db := testutil.SetupTestDB(t)
	store := tenant.NewPostgresStore(db)

	_, err := store.Get(context.Background(), "nonexistent")
	if err != errs.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestPostgresStore_List(t *testing.T) {
	db := testutil.SetupTestDB(t)
	store := tenant.NewPostgresStore(db)
	ctx := context.Background()

	store.Create(ctx, domain.Tenant{Name: "Tenant A"})
	store.Create(ctx, domain.Tenant{Name: "Tenant B"})

	all, err := store.List(ctx, tenant.ListFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("list count: got %d, want 2", len(all))
	}

	// Filter by status
	active, err := store.List(ctx, tenant.ListFilter{Status: "active"})
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	if len(active) != 2 {
		t.Errorf("active count: got %d, want 2", len(active))
	}
}

func TestPostgresStore_UpdateStatus(t *testing.T) {
	db := testutil.SetupTestDB(t)
	store := tenant.NewPostgresStore(db)
	ctx := context.Background()

	created, _ := store.Create(ctx, domain.Tenant{Name: "Test"})

	updated, err := store.UpdateStatus(ctx, created.ID, domain.TenantStatusSuspended)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Status != domain.TenantStatusSuspended {
		t.Errorf("status: got %q, want suspended", updated.Status)
	}

	// Verify persisted
	got, _ := store.Get(ctx, created.ID)
	if got.Status != domain.TenantStatusSuspended {
		t.Errorf("persisted status: got %q, want suspended", got.Status)
	}
}
