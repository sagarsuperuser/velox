package customer_test

import (
	"context"
	"testing"

	"github.com/sagarsuperuser/velox/internal/customer"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/testutil"
)

func TestPostgresStore_CreateAndGet(t *testing.T) {
	db := testutil.SetupTestDB(t)
	store := customer.NewPostgresStore(db)
	ctx := context.Background()
	tenantID := testutil.CreateTestTenant(t, db, "Test Tenant")

	created, err := store.Create(ctx, tenantID, domain.Customer{
		ExternalID:  "cus_ext_001",
		DisplayName: "Acme Corp",
		Email:       "billing@acme.com",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID == "" {
		t.Fatal("ID should be generated")
	}
	if created.TenantID != tenantID {
		t.Errorf("tenant_id: got %q, want %q", created.TenantID, tenantID)
	}

	got, err := store.Get(ctx, tenantID, created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ExternalID != "cus_ext_001" {
		t.Errorf("external_id: got %q", got.ExternalID)
	}
	if got.Email != "billing@acme.com" {
		t.Errorf("email: got %q", got.Email)
	}
}

func TestPostgresStore_UniqueExternalID(t *testing.T) {
	db := testutil.SetupTestDB(t)
	store := customer.NewPostgresStore(db)
	ctx := context.Background()
	tenantID := testutil.CreateTestTenant(t, db, "Test")

	_, _ = store.Create(ctx, tenantID, domain.Customer{ExternalID: "dup", DisplayName: "First"})

	_, err := store.Create(ctx, tenantID, domain.Customer{ExternalID: "dup", DisplayName: "Second"})
	if err == nil {
		t.Fatal("expected error for duplicate external_id")
	}
}

func TestPostgresStore_RLSIsolation(t *testing.T) {
	db := testutil.SetupTestDB(t)
	store := customer.NewPostgresStore(db)
	ctx := context.Background()

	tenant1 := testutil.CreateTestTenant(t, db, "Tenant 1")
	tenant2 := testutil.CreateTestTenant(t, db, "Tenant 2")

	// Create customer in tenant1
	cust, _ := store.Create(ctx, tenant1, domain.Customer{ExternalID: "shared_id", DisplayName: "Tenant1 Customer"})

	// Should be visible from tenant1
	_, err := store.Get(ctx, tenant1, cust.ID)
	if err != nil {
		t.Fatalf("tenant1 should see its own customer: %v", err)
	}

	// Should NOT be visible from tenant2 (RLS)
	_, err = store.Get(ctx, tenant2, cust.ID)
	if err != errs.ErrNotFound {
		t.Errorf("tenant2 should NOT see tenant1's customer, got: %v", err)
	}

	// List from tenant2 should be empty
	list, _, err := store.List(ctx, customer.ListFilter{TenantID: tenant2})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("tenant2 list should be empty, got %d", len(list))
	}
}

func TestPostgresStore_ListWithFilters(t *testing.T) {
	db := testutil.SetupTestDB(t)
	store := customer.NewPostgresStore(db)
	ctx := context.Background()
	tenantID := testutil.CreateTestTenant(t, db, "Test")

	_, _ = store.Create(ctx, tenantID, domain.Customer{ExternalID: "a", DisplayName: "Alpha"})
	_, _ = store.Create(ctx, tenantID, domain.Customer{ExternalID: "b", DisplayName: "Beta"})
	_, _ = store.Create(ctx, tenantID, domain.Customer{ExternalID: "c", DisplayName: "Charlie"})

	// All
	all, total, err := store.List(ctx, customer.ListFilter{TenantID: tenantID})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if total != 3 {
		t.Errorf("total: got %d, want 3", total)
	}
	if len(all) != 3 {
		t.Errorf("items: got %d, want 3", len(all))
	}

	// By external_id
	filtered, _, err := store.List(ctx, customer.ListFilter{TenantID: tenantID, ExternalID: "b"})
	if err != nil {
		t.Fatalf("list filtered: %v", err)
	}
	if len(filtered) != 1 || filtered[0].DisplayName != "Beta" {
		t.Errorf("filtered: expected Beta, got %v", filtered)
	}

	// Pagination
	page, _, err := store.List(ctx, customer.ListFilter{TenantID: tenantID, Limit: 2})
	if err != nil {
		t.Fatalf("list paginated: %v", err)
	}
	if len(page) != 2 {
		t.Errorf("page: got %d, want 2", len(page))
	}
}

func TestPostgresStore_BillingProfile(t *testing.T) {
	db := testutil.SetupTestDB(t)
	store := customer.NewPostgresStore(db)
	ctx := context.Background()
	tenantID := testutil.CreateTestTenant(t, db, "Test")

	cust, _ := store.Create(ctx, tenantID, domain.Customer{ExternalID: "bp_test", DisplayName: "BP"})

	// Upsert
	bp, err := store.UpsertBillingProfile(ctx, tenantID, domain.CustomerBillingProfile{
		CustomerID:    cust.ID,
		LegalName:     "BP Inc.",
		Country:       "US",
		Currency:      "USD",
		ProfileStatus: domain.BillingProfileReady,
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if bp.LegalName != "BP Inc." {
		t.Errorf("legal_name: got %q", bp.LegalName)
	}

	// Get
	got, err := store.GetBillingProfile(ctx, tenantID, cust.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Country != "US" {
		t.Errorf("country: got %q", got.Country)
	}

	// Update (upsert again)
	bp2, err := store.UpsertBillingProfile(ctx, tenantID, domain.CustomerBillingProfile{
		CustomerID:    cust.ID,
		LegalName:     "BP LLC",
		Country:       "CA",
		Currency:      "CAD",
		ProfileStatus: domain.BillingProfileReady,
	})
	if err != nil {
		t.Fatalf("upsert update: %v", err)
	}
	if bp2.LegalName != "BP LLC" {
		t.Errorf("updated legal_name: got %q", bp2.LegalName)
	}
	if bp2.Country != "CA" {
		t.Errorf("updated country: got %q", bp2.Country)
	}
}

func TestPostgresStore_Update(t *testing.T) {
	db := testutil.SetupTestDB(t)
	store := customer.NewPostgresStore(db)
	ctx := context.Background()
	tenantID := testutil.CreateTestTenant(t, db, "Test")

	cust, _ := store.Create(ctx, tenantID, domain.Customer{ExternalID: "upd", DisplayName: "Original"})

	cust.DisplayName = "Updated"
	cust.Email = "new@example.com"
	updated, err := store.Update(ctx, tenantID, cust)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.DisplayName != "Updated" {
		t.Errorf("display_name: got %q", updated.DisplayName)
	}
	if updated.Email != "new@example.com" {
		t.Errorf("email: got %q", updated.Email)
	}
}
