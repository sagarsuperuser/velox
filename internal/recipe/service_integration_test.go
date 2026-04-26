package recipe

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/dunning"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
	"github.com/sagarsuperuser/velox/internal/pricing"
	"github.com/sagarsuperuser/velox/internal/testutil"
	"github.com/sagarsuperuser/velox/internal/webhook"
)

// recipeIntegrationFixture wires the real per-domain stores and services
// against a clean test database. Returned pieces are everything the tests
// need to instantiate a recipe and then count what landed in each table.
type recipeIntegrationFixture struct {
	db         *postgres.DB
	svc        *Service
	store      *PostgresStore
	pricingSvc *pricing.Service
	dunningSvc *dunning.Service
	webhookSvc *webhook.Service
}

func newRecipeFixture(t *testing.T) *recipeIntegrationFixture {
	t.Helper()
	db := testutil.SetupTestDB(t)
	registry, err := Load()
	if err != nil {
		t.Fatalf("load recipe registry: %v", err)
	}
	pricingSvc := pricing.NewService(pricing.NewPostgresStore(db))
	// dunning needs no retrier for UpsertPolicyTx — passing nil is safe;
	// the recipes flow never invokes RetryPayment.
	dunningSvc := dunning.NewService(dunning.NewPostgresStore(db), nil, nil)
	webhookSvc := webhook.NewService(webhook.NewPostgresStore(db), nil)
	store := NewPostgresStore(db)
	svc := NewService(db, store, registry, pricingSvc, dunningSvc, webhookSvc)
	return &recipeIntegrationFixture{
		db:         db,
		svc:        svc,
		store:      store,
		pricingSvc: pricingSvc,
		dunningSvc: dunningSvc,
		webhookSvc: webhookSvc,
	}
}

// countRows runs a SELECT count(*) inside a tenant-scoped tx so RLS
// permits the read. Returns whatever the row count is for that tenant —
// other tenants' rows are filtered by RLS anyway.
func countRows(t *testing.T, db *postgres.DB, tenantID, table string) int {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), postgres.TxTenant, tenantID)
	if err != nil {
		t.Fatalf("begin count tx: %v", err)
	}
	defer postgres.Rollback(tx)
	var n int
	q := fmt.Sprintf("SELECT count(*) FROM %s", table)
	if err := tx.QueryRowContext(context.Background(), q).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// TestService_Instantiate_BuildsFullGraph mirrors the design-doc acceptance
// test: anthropic_style produces 1 meter, 9 rating rules, 9 pricing rules,
// 1 plan, 1 dunning policy, 1 webhook endpoint. Counts come from direct
// SQL so there's no test-side double-counting via the same Store path the
// production code wrote through.
func TestService_Instantiate_BuildsFullGraph(t *testing.T) {
	f := newRecipeFixture(t)
	tenantID := testutil.CreateTestTenant(t, f.db, "anthropic install")
	ctx := context.Background()

	inst, err := f.svc.Instantiate(ctx, tenantID, "anthropic_style", nil, InstantiateOptions{
		CreatedBy: "operator@example.com",
	})
	if err != nil {
		t.Fatalf("Instantiate: %v", err)
	}
	if inst.ID == "" {
		t.Fatal("expected RecipeInstance ID")
	}
	if inst.RecipeKey != "anthropic_style" {
		t.Errorf("RecipeKey: got %q", inst.RecipeKey)
	}
	if got, want := len(inst.CreatedObjects.MeterIDs), 1; got != want {
		t.Errorf("CreatedObjects.MeterIDs: got %d, want %d", got, want)
	}
	if got, want := len(inst.CreatedObjects.RatingRuleIDs), 9; got != want {
		t.Errorf("CreatedObjects.RatingRuleIDs: got %d, want %d", got, want)
	}
	if got, want := len(inst.CreatedObjects.PricingRuleIDs), 9; got != want {
		t.Errorf("CreatedObjects.PricingRuleIDs: got %d, want %d", got, want)
	}
	if got, want := len(inst.CreatedObjects.PlanIDs), 1; got != want {
		t.Errorf("CreatedObjects.PlanIDs: got %d, want %d", got, want)
	}
	if inst.CreatedObjects.DunningPolicyID == "" {
		t.Error("expected DunningPolicyID")
	}
	if inst.CreatedObjects.WebhookEndpointID == "" {
		t.Error("expected WebhookEndpointID")
	}

	// Verify against the actual rows persisted in each domain's table.
	if n := countRows(t, f.db, tenantID, "meters"); n != 1 {
		t.Errorf("meters row count: got %d, want 1", n)
	}
	if n := countRows(t, f.db, tenantID, "rating_rule_versions"); n != 9 {
		t.Errorf("rating_rule_versions row count: got %d, want 9", n)
	}
	if n := countRows(t, f.db, tenantID, "meter_pricing_rules"); n != 9 {
		t.Errorf("meter_pricing_rules row count: got %d, want 9", n)
	}
	if n := countRows(t, f.db, tenantID, "plans"); n != 1 {
		t.Errorf("plans row count: got %d, want 1", n)
	}
	if n := countRows(t, f.db, tenantID, "dunning_policies"); n != 1 {
		t.Errorf("dunning_policies row count: got %d, want 1", n)
	}
	if n := countRows(t, f.db, tenantID, "webhook_endpoints"); n != 1 {
		t.Errorf("webhook_endpoints row count: got %d, want 1", n)
	}
	if n := countRows(t, f.db, tenantID, "recipe_instances"); n != 1 {
		t.Errorf("recipe_instances row count: got %d, want 1", n)
	}
}

// TestService_Instantiate_Idempotent verifies the second Instantiate call
// for the same (tenant, recipe_key) returns AlreadyExists with the
// existing instance ID and creates no new rows. The pre-check inside the
// tx is the first line of defence; the UNIQUE index in postgres_integration
// is the second.
func TestService_Instantiate_Idempotent(t *testing.T) {
	f := newRecipeFixture(t)
	tenantID := testutil.CreateTestTenant(t, f.db, "idempotent install")
	ctx := context.Background()

	first, err := f.svc.Instantiate(ctx, tenantID, "anthropic_style", nil, InstantiateOptions{})
	if err != nil {
		t.Fatalf("first Instantiate: %v", err)
	}

	_, err = f.svc.Instantiate(ctx, tenantID, "anthropic_style", nil, InstantiateOptions{})
	if !errors.Is(err, errs.ErrAlreadyExists) {
		t.Fatalf("expected ErrAlreadyExists on second instantiate, got %v", err)
	}

	// No second graph was built — counts unchanged.
	if n := countRows(t, f.db, tenantID, "rating_rule_versions"); n != 9 {
		t.Errorf("rating_rule_versions after duplicate Instantiate: got %d, want 9", n)
	}
	if n := countRows(t, f.db, tenantID, "recipe_instances"); n != 1 {
		t.Errorf("recipe_instances after duplicate Instantiate: got %d, want 1", n)
	}

	got, err := f.store.GetByKey(ctx, tenantID, "anthropic_style")
	if err != nil {
		t.Fatalf("GetByKey: %v", err)
	}
	if got.ID != first.ID {
		t.Errorf("instance ID drifted: got %q, want %q", got.ID, first.ID)
	}
}

// failingPricingWriter wraps the real pricing writer and fails on the
// nth call to UpsertMeterPricingRuleTx. Used to inject a mid-graph failure
// so the rollback path is exercised against real DB rows.
type failingPricingWriter struct {
	inner    PricingWriter
	failAt   int
	pricingN int
}

func (f *failingPricingWriter) CreateRatingRuleTx(ctx context.Context, tx *sql.Tx, tenantID string, rule domain.RatingRuleVersion) (domain.RatingRuleVersion, error) {
	return f.inner.CreateRatingRuleTx(ctx, tx, tenantID, rule)
}
func (f *failingPricingWriter) CreateMeterTx(ctx context.Context, tx *sql.Tx, tenantID string, m domain.Meter) (domain.Meter, error) {
	return f.inner.CreateMeterTx(ctx, tx, tenantID, m)
}
func (f *failingPricingWriter) CreatePlanTx(ctx context.Context, tx *sql.Tx, tenantID string, p domain.Plan) (domain.Plan, error) {
	return f.inner.CreatePlanTx(ctx, tx, tenantID, p)
}
func (f *failingPricingWriter) UpsertMeterPricingRuleTx(ctx context.Context, tx *sql.Tx, tenantID string, rule domain.MeterPricingRule) (domain.MeterPricingRule, error) {
	f.pricingN++
	if f.pricingN == f.failAt {
		return domain.MeterPricingRule{}, errors.New("synthetic failure at pricing rule N")
	}
	return f.inner.UpsertMeterPricingRuleTx(ctx, tx, tenantID, rule)
}

// TestService_Instantiate_AtomicityRollback fails partway through the
// graph build and verifies zero rows survive — the contract is "fully
// installed or not installed at all", with no orphan meters or rating
// rules to clean up by hand.
func TestService_Instantiate_AtomicityRollback(t *testing.T) {
	f := newRecipeFixture(t)
	tenantID := testutil.CreateTestTenant(t, f.db, "atomicity rollback")
	ctx := context.Background()

	failing := &failingPricingWriter{inner: f.pricingSvc, failAt: 5}
	registry, err := Load()
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	svc := NewService(f.db, NewPostgresStore(f.db), registry, failing, f.dunningSvc, f.webhookSvc)

	_, err = svc.Instantiate(ctx, tenantID, "anthropic_style", nil, InstantiateOptions{})
	if err == nil {
		t.Fatal("expected synthetic failure to surface")
	}

	for _, tbl := range []string{
		"meters", "rating_rule_versions", "meter_pricing_rules",
		"plans", "dunning_policies", "webhook_endpoints", "recipe_instances",
	} {
		if n := countRows(t, f.db, tenantID, tbl); n != 0 {
			t.Errorf("%s after rollback: got %d, want 0", tbl, n)
		}
	}
}

// TestService_Instantiate_RLSIsolation installs anthropic_style for tenant
// A and verifies tenant B sees no instance — both via the recipe store
// (GetByKey returns ErrNotFound) and via ListRecipes (Instantiated is nil
// for the same recipe key).
func TestService_Instantiate_RLSIsolation(t *testing.T) {
	f := newRecipeFixture(t)
	tenantA := testutil.CreateTestTenant(t, f.db, "tenant A")
	tenantB := testutil.CreateTestTenant(t, f.db, "tenant B")
	ctx := context.Background()

	if _, err := f.svc.Instantiate(ctx, tenantA, "anthropic_style", nil, InstantiateOptions{}); err != nil {
		t.Fatalf("Instantiate for A: %v", err)
	}

	if _, err := f.store.GetByKey(ctx, tenantB, "anthropic_style"); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("tenant B should not see A's instance: got %v", err)
	}

	items, err := f.svc.ListRecipes(ctx, tenantB)
	if err != nil {
		t.Fatalf("ListRecipes(B): %v", err)
	}
	for _, item := range items {
		if item.Key == "anthropic_style" && item.Instantiated != nil {
			t.Errorf("tenant B sees A's anthropic_style installed: %+v", item.Instantiated)
		}
	}
}

// TestService_Instantiate_PreviewParity verifies Preview and Instantiate
// render the same logical graph (same counts, same rule keys, same plan
// code). IDs naturally differ — Preview has none.
func TestService_Instantiate_PreviewParity(t *testing.T) {
	f := newRecipeFixture(t)
	tenantID := testutil.CreateTestTenant(t, f.db, "preview parity")
	ctx := context.Background()

	overrides := map[string]any{"currency": "EUR", "plan_code": "ai_eur", "plan_name": "AI EUR"}

	previewed, err := f.svc.Preview(ctx, "anthropic_style", overrides)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	inst, err := f.svc.Instantiate(ctx, tenantID, "anthropic_style", overrides, InstantiateOptions{})
	if err != nil {
		t.Fatalf("Instantiate: %v", err)
	}

	if got, want := len(inst.CreatedObjects.RatingRuleIDs), len(previewed.Objects.RatingRules); got != want {
		t.Errorf("rating-rule count drift: instantiate=%d, preview=%d", got, want)
	}
	if got, want := len(inst.CreatedObjects.PricingRuleIDs), len(previewed.Objects.PricingRules); got != want {
		t.Errorf("pricing-rule count drift: instantiate=%d, preview=%d", got, want)
	}
	plans, err := f.pricingSvc.ListPlans(ctx, tenantID)
	if err != nil {
		t.Fatalf("ListPlans: %v", err)
	}
	if len(plans) != 1 {
		t.Fatalf("plans: got %d, want 1", len(plans))
	}
	if plans[0].Code != "ai_eur" {
		t.Errorf("plan code: got %q, want ai_eur", plans[0].Code)
	}
	if plans[0].Currency != "EUR" {
		t.Errorf("plan currency: got %q, want EUR", plans[0].Currency)
	}
}

// TestService_Uninstall_RemovesInstanceOnly proves the documented v1
// behaviour: Uninstall deletes the recipe_instance row but leaves the
// resources the recipe created (plans, meters, dunning policy, webhook
// endpoint) alone — operators own them once they exist, and silent
// cascade could lose live billing data.
func TestService_Uninstall_RemovesInstanceOnly(t *testing.T) {
	f := newRecipeFixture(t)
	tenantID := testutil.CreateTestTenant(t, f.db, "uninstall keeps resources")
	ctx := context.Background()

	inst, err := f.svc.Instantiate(ctx, tenantID, "anthropic_style", nil, InstantiateOptions{})
	if err != nil {
		t.Fatalf("Instantiate: %v", err)
	}

	if err := f.svc.Uninstall(ctx, tenantID, inst.ID); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}

	if n := countRows(t, f.db, tenantID, "recipe_instances"); n != 0 {
		t.Errorf("recipe_instances after Uninstall: got %d, want 0", n)
	}
	// The downstream resources stick around.
	if n := countRows(t, f.db, tenantID, "plans"); n != 1 {
		t.Errorf("plans after Uninstall: got %d, want 1 (resources persist)", n)
	}
	if n := countRows(t, f.db, tenantID, "meters"); n != 1 {
		t.Errorf("meters after Uninstall: got %d, want 1 (resources persist)", n)
	}
	if n := countRows(t, f.db, tenantID, "rating_rule_versions"); n != 9 {
		t.Errorf("rating_rule_versions after Uninstall: got %d, want 9 (resources persist)", n)
	}
}
