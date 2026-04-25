package recipe

import (
	"context"
	"errors"
	"testing"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
	"github.com/sagarsuperuser/velox/internal/testutil"
)

// TestPostgresStore_CreateAndGet exercises the round-trip: insert a row
// inside a Tx, read it back via the convenience GetByKey wrapper, verify
// every field hydrates including the nested CreatedObjects JSONB.
func TestPostgresStore_CreateAndGet(t *testing.T) {
	db := testutil.SetupTestDB(t)
	tenantID := testutil.CreateTestTenant(t, db, "recipe test")
	store := NewPostgresStore(db)
	ctx := context.Background()

	tx, err := db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer postgres.Rollback(tx)

	created, err := store.CreateTx(ctx, tx, domain.RecipeInstance{
		TenantID:      tenantID,
		RecipeKey:     "anthropic_style",
		RecipeVersion: "1.0.0",
		Overrides:     map[string]any{"currency": "USD", "plan_name": "AI API"},
		CreatedObjects: domain.CreatedObjects{
			MeterIDs:          []string{"vlx_mtr_aaa"},
			PlanIDs:           []string{"vlx_pln_bbb"},
			DunningPolicyID:   "vlx_dpol_ccc",
			WebhookEndpointID: "vlx_whk_ddd",
		},
		CreatedBy: "operator@example.com",
	})
	if err != nil {
		t.Fatalf("CreateTx: %v", err)
	}
	if created.ID == "" {
		t.Error("expected ID to be populated")
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	got, err := store.GetByKey(ctx, tenantID, "anthropic_style")
	if err != nil {
		t.Fatalf("GetByKey: %v", err)
	}
	if got.ID != created.ID {
		t.Errorf("ID mismatch: got %q want %q", got.ID, created.ID)
	}
	if got.RecipeVersion != "1.0.0" {
		t.Errorf("Version: got %q", got.RecipeVersion)
	}
	if got.Overrides["currency"] != "USD" {
		t.Errorf("Overrides: got %v", got.Overrides)
	}
	if got.CreatedObjects.DunningPolicyID != "vlx_dpol_ccc" {
		t.Errorf("CreatedObjects.DunningPolicyID: got %q", got.CreatedObjects.DunningPolicyID)
	}
	if got.CreatedBy != "operator@example.com" {
		t.Errorf("CreatedBy: got %q", got.CreatedBy)
	}
}

// TestPostgresStore_GetByKeyNotFound asserts the unambiguous miss signal so
// Service.Instantiate can branch on errs.ErrNotFound without sniffing
// sql.ErrNoRows directly.
func TestPostgresStore_GetByKeyNotFound(t *testing.T) {
	db := testutil.SetupTestDB(t)
	tenantID := testutil.CreateTestTenant(t, db, "recipe miss")
	store := NewPostgresStore(db)

	_, err := store.GetByKey(context.Background(), tenantID, "openai_style")
	if !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// TestPostgresStore_DeleteByKeyTx exercises the force-re-instantiate path:
// a no-op delete on an unknown key returns nil (the caller doesn't need to
// know whether the row existed); a real delete removes the row so a follow-
// up GetByKey misses.
func TestPostgresStore_DeleteByKeyTx(t *testing.T) {
	db := testutil.SetupTestDB(t)
	tenantID := testutil.CreateTestTenant(t, db, "recipe delete")
	store := NewPostgresStore(db)
	ctx := context.Background()

	// Unknown key — should not error.
	tx, err := db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := store.DeleteByKeyTx(ctx, tx, tenantID, "missing"); err != nil {
		t.Errorf("DeleteByKeyTx on unknown should be nil, got %v", err)
	}
	_ = tx.Rollback()

	// Real delete.
	tx, err = db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		t.Fatalf("begin 2: %v", err)
	}
	if _, err := store.CreateTx(ctx, tx, domain.RecipeInstance{
		TenantID: tenantID, RecipeKey: "openai_style", RecipeVersion: "1.0.0",
	}); err != nil {
		t.Fatalf("CreateTx: %v", err)
	}
	if err := store.DeleteByKeyTx(ctx, tx, tenantID, "openai_style"); err != nil {
		t.Fatalf("DeleteByKeyTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if _, err := store.GetByKey(ctx, tenantID, "openai_style"); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

// TestPostgresStore_UniqueByTenantKey enforces the idempotency guarantee
// at the schema layer: two CreateTx calls with the same (tenant, key)
// must fail at INSERT (UNIQUE constraint), independent of any Service-
// level pre-check that might race.
func TestPostgresStore_UniqueByTenantKey(t *testing.T) {
	db := testutil.SetupTestDB(t)
	tenantID := testutil.CreateTestTenant(t, db, "recipe unique")
	store := NewPostgresStore(db)
	ctx := context.Background()

	tx1, err := db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := store.CreateTx(ctx, tx1, domain.RecipeInstance{
		TenantID: tenantID, RecipeKey: "b2b_saas_pro", RecipeVersion: "1.0.0",
	}); err != nil {
		t.Fatalf("first CreateTx: %v", err)
	}
	if err := tx1.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	tx2, err := db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		t.Fatalf("begin 2: %v", err)
	}
	defer postgres.Rollback(tx2)
	if _, err := store.CreateTx(ctx, tx2, domain.RecipeInstance{
		TenantID: tenantID, RecipeKey: "b2b_saas_pro", RecipeVersion: "1.0.0",
	}); err == nil {
		t.Fatal("expected UNIQUE-violation error on duplicate (tenant, key) insert")
	}
}
