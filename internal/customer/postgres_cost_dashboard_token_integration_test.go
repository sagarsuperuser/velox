package customer_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/customer"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/testutil"
)

// TestPostgresStore_SetCostDashboardToken_RoundTrip is the canonical
// happy path: write a token via the store and read the same customer
// back via GetByCostDashboardToken. Confirms the column is wired
// through both sides of the postgres adapter and that the cross-tenant
// TxBypass lookup actually works (the lookup ignores tenant_id by
// design — the token IS the credential).
func TestPostgresStore_SetCostDashboardToken_RoundTrip(t *testing.T) {
	db := testutil.SetupTestDB(t)
	store := customer.NewPostgresStore(db)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tenantID := testutil.CreateTestTenant(t, db, "CostDash RoundTrip")
	cust, err := store.Create(ctx, tenantID, domain.Customer{
		ExternalID:  "cus_costdash_rt",
		DisplayName: "RoundTrip Co",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	token, err := customer.GenerateCostDashboardToken()
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	if err := store.SetCostDashboardToken(ctx, tenantID, cust.ID, token); err != nil {
		t.Fatalf("set token: %v", err)
	}

	got, err := store.GetByCostDashboardToken(ctx, token)
	if err != nil {
		t.Fatalf("get by token: %v", err)
	}
	if got.ID != cust.ID {
		t.Errorf("customer id: got %q, want %q", got.ID, cust.ID)
	}
	if got.TenantID != tenantID {
		t.Errorf("tenant id: got %q, want %q", got.TenantID, tenantID)
	}
	if got.CostDashboardToken != token {
		t.Errorf("token round-trip: got %q, want %q", got.CostDashboardToken, token)
	}
}

// TestPostgresStore_GetByCostDashboardToken_UnknownReturnsNotFound
// covers the negative path: a well-formed but unknown token resolves
// to ErrNotFound, never to an empty-customer result. Critical because
// the public handler maps ErrNotFound → 404 and anything else → 500.
func TestPostgresStore_GetByCostDashboardToken_UnknownReturnsNotFound(t *testing.T) {
	db := testutil.SetupTestDB(t)
	store := customer.NewPostgresStore(db)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	bogus := customer.CostDashboardTokenPrefix + strings.Repeat("0", 64)
	_, err := store.GetByCostDashboardToken(ctx, bogus)
	if !errors.Is(err, errs.ErrNotFound) {
		t.Errorf("unknown token: got %v, want ErrNotFound", err)
	}

	_, err = store.GetByCostDashboardToken(ctx, "")
	if !errors.Is(err, errs.ErrNotFound) {
		t.Errorf("empty token: got %v, want ErrNotFound", err)
	}
}

// TestPostgresStore_SetCostDashboardToken_RotationInvalidatesPrevious
// is the rotation-as-invalidation guarantee. After re-setting a fresh
// token, the previous token must no longer resolve any customer — at
// the DB level, not just at the in-memory mock layer.
func TestPostgresStore_SetCostDashboardToken_RotationInvalidatesPrevious(t *testing.T) {
	db := testutil.SetupTestDB(t)
	store := customer.NewPostgresStore(db)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tenantID := testutil.CreateTestTenant(t, db, "CostDash Rotate")
	cust, err := store.Create(ctx, tenantID, domain.Customer{
		ExternalID:  "cus_costdash_rot",
		DisplayName: "Rotate Co",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	tokenOld, _ := customer.GenerateCostDashboardToken()
	tokenNew, _ := customer.GenerateCostDashboardToken()
	if tokenOld == tokenNew {
		t.Fatal("entropy: two consecutive token generations collided (≈0 probability — bug)")
	}

	if err := store.SetCostDashboardToken(ctx, tenantID, cust.ID, tokenOld); err != nil {
		t.Fatalf("set old: %v", err)
	}
	if err := store.SetCostDashboardToken(ctx, tenantID, cust.ID, tokenNew); err != nil {
		t.Fatalf("set new: %v", err)
	}

	if _, err := store.GetByCostDashboardToken(ctx, tokenOld); !errors.Is(err, errs.ErrNotFound) {
		t.Errorf("old token after rotation: got %v, want ErrNotFound", err)
	}
	got, err := store.GetByCostDashboardToken(ctx, tokenNew)
	if err != nil {
		t.Fatalf("new token: %v", err)
	}
	if got.ID != cust.ID {
		t.Errorf("new token resolved to wrong customer: got %q, want %q", got.ID, cust.ID)
	}
}

// TestPostgresStore_SetCostDashboardToken_EmptyClearsToken covers the
// "revoke without minting a replacement" path: writing an empty string
// stores NULL and the previous token stops resolving. Useful if an
// operator wants to nuke all public access for a customer who hasn't
// yet been issued a replacement embed URL.
func TestPostgresStore_SetCostDashboardToken_EmptyClearsToken(t *testing.T) {
	db := testutil.SetupTestDB(t)
	store := customer.NewPostgresStore(db)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tenantID := testutil.CreateTestTenant(t, db, "CostDash Clear")
	cust, err := store.Create(ctx, tenantID, domain.Customer{
		ExternalID:  "cus_costdash_clear",
		DisplayName: "Clear Co",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	token, _ := customer.GenerateCostDashboardToken()
	if err := store.SetCostDashboardToken(ctx, tenantID, cust.ID, token); err != nil {
		t.Fatalf("set token: %v", err)
	}
	if err := store.SetCostDashboardToken(ctx, tenantID, cust.ID, ""); err != nil {
		t.Fatalf("clear token: %v", err)
	}

	if _, err := store.GetByCostDashboardToken(ctx, token); !errors.Is(err, errs.ErrNotFound) {
		t.Errorf("cleared token still resolved: got %v, want ErrNotFound", err)
	}
	// Customer Get must also surface the cleared state.
	got, err := store.Get(ctx, tenantID, cust.ID)
	if err != nil {
		t.Fatalf("get after clear: %v", err)
	}
	if got.CostDashboardToken != "" {
		t.Errorf("token field after clear: got %q, want empty", got.CostDashboardToken)
	}
}

// TestPostgresStore_SetCostDashboardToken_NotFound covers the
// "rotation against a non-existent customer" path. The service-layer
// guard is a Get-first, but the store must still fail-safe if a future
// caller hits Set directly.
func TestPostgresStore_SetCostDashboardToken_NotFound(t *testing.T) {
	db := testutil.SetupTestDB(t)
	store := customer.NewPostgresStore(db)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tenantID := testutil.CreateTestTenant(t, db, "CostDash NotFound")
	token, _ := customer.GenerateCostDashboardToken()
	err := store.SetCostDashboardToken(ctx, tenantID, "vlx_cus_nonexistent", token)
	if !errors.Is(err, errs.ErrNotFound) {
		t.Errorf("set on missing id: got %v, want ErrNotFound", err)
	}
}
