package recipe

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

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
	tx, err := db.BeginTx(postgres.WithLivemode(context.Background(), false), postgres.TxTenant, tenantID)
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
	ctx := postgres.WithLivemode(context.Background(), false)

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
	// anthropic_style v2 (ADR-044): 4 models × 5 token roles.
	if got, want := len(inst.CreatedObjects.RatingRuleIDs), 35; got != want {
		t.Errorf("CreatedObjects.RatingRuleIDs: got %d, want %d", got, want)
	}
	if got, want := len(inst.CreatedObjects.PricingRuleIDs), 35; got != want {
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
	if n := countRows(t, f.db, tenantID, "rating_rule_versions"); n != 35 {
		t.Errorf("rating_rule_versions row count: got %d, want 35", n)
	}
	if n := countRows(t, f.db, tenantID, "meter_pricing_rules"); n != 35 {
		t.Errorf("meter_pricing_rules row count: got %d, want 35", n)
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
	ctx := postgres.WithLivemode(context.Background(), false)

	first, err := f.svc.Instantiate(ctx, tenantID, "anthropic_style", nil, InstantiateOptions{})
	if err != nil {
		t.Fatalf("first Instantiate: %v", err)
	}

	_, err = f.svc.Instantiate(ctx, tenantID, "anthropic_style", nil, InstantiateOptions{})
	if !errors.Is(err, errs.ErrAlreadyExists) {
		t.Fatalf("expected ErrAlreadyExists on second instantiate, got %v", err)
	}

	// No second graph was built — counts unchanged (anthropic_style v3 = 35 rules).
	if n := countRows(t, f.db, tenantID, "rating_rule_versions"); n != 35 {
		t.Errorf("rating_rule_versions after duplicate Instantiate: got %d, want 35", n)
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
func (f *failingPricingWriter) GetRuleByKeyAsOf(ctx context.Context, tenantID, ruleKey string, asOf time.Time) (domain.RatingRuleVersion, error) {
	return f.inner.GetRuleByKeyAsOf(ctx, tenantID, ruleKey, asOf)
}
func (f *failingPricingWriter) GetMeterByKey(ctx context.Context, tenantID, key string) (domain.Meter, error) {
	return f.inner.GetMeterByKey(ctx, tenantID, key)
}
func (f *failingPricingWriter) ListPlans(ctx context.Context, tenantID string) ([]domain.Plan, error) {
	return f.inner.ListPlans(ctx, tenantID)
}

// TestService_Instantiate_AtomicityRollback fails partway through the
// graph build and verifies zero rows survive — the contract is "fully
// installed or not installed at all", with no orphan meters or rating
// rules to clean up by hand.
func TestService_Instantiate_AtomicityRollback(t *testing.T) {
	f := newRecipeFixture(t)
	tenantID := testutil.CreateTestTenant(t, f.db, "atomicity rollback")
	ctx := postgres.WithLivemode(context.Background(), false)

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
	ctx := postgres.WithLivemode(context.Background(), false)

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
	ctx := postgres.WithLivemode(context.Background(), false)

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
	ctx := postgres.WithLivemode(context.Background(), false)

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
	// anthropic_style v3 (2026-07-05 refresh): 7 models × 5 token roles = 35 rating rules.
	if n := countRows(t, f.db, tenantID, "rating_rule_versions"); n != 35 {
		t.Errorf("rating_rule_versions after Uninstall: got %d, want 35 (resources persist)", n)
	}
}

// TestService_ReinstallAfterUninstall_AdoptsExistingGraph: Uninstall
// keeps the created objects (the operator owns them), so reinstall must
// RECONNECT to them by natural key — pre-ADR-070 it 409ed on the first
// hardcoded-Version:1 rating rule ("the operator is told they own the
// objects, then punished for owning them"). Adoption never duplicates:
// object counts stay flat across the round trip.
func TestService_ReinstallAfterUninstall_AdoptsExistingGraph(t *testing.T) {
	f := newRecipeFixture(t)
	tenantID := testutil.CreateTestTenant(t, f.db, "reinstall adopt")
	ctx := postgres.WithLivemode(context.Background(), false)

	first, err := f.svc.Instantiate(ctx, tenantID, "anthropic_style", nil, InstantiateOptions{})
	if err != nil {
		t.Fatalf("first Instantiate: %v", err)
	}
	rulesAfterFirst := countRows(t, f.db, tenantID, "rating_rule_versions")
	metersAfterFirst := countRows(t, f.db, tenantID, "meters")
	plansAfterFirst := countRows(t, f.db, tenantID, "plans")

	if err := f.svc.Uninstall(ctx, tenantID, first.ID); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}

	second, err := f.svc.Instantiate(ctx, tenantID, "anthropic_style", nil, InstantiateOptions{})
	if err != nil {
		t.Fatalf("reinstall after uninstall: %v", err)
	}
	if second.ID == first.ID {
		t.Error("reinstall returned the deleted instance id")
	}

	// Adoption, not duplication: the graph is reused, not rebuilt.
	if n := countRows(t, f.db, tenantID, "rating_rule_versions"); n != rulesAfterFirst {
		t.Errorf("rating_rule_versions after reinstall: got %d, want %d (adopted, not republished)", n, rulesAfterFirst)
	}
	if n := countRows(t, f.db, tenantID, "meters"); n != metersAfterFirst {
		t.Errorf("meters after reinstall: got %d, want %d", n, metersAfterFirst)
	}
	if n := countRows(t, f.db, tenantID, "plans"); n != plansAfterFirst {
		t.Errorf("plans after reinstall: got %d, want %d", n, plansAfterFirst)
	}
	if n := countRows(t, f.db, tenantID, "recipe_instances"); n != 1 {
		t.Errorf("recipe_instances after reinstall: got %d, want 1", n)
	}
}

// TestService_Instantiate_ReusesExistingPlan is the ADR-084 regression: a
// pre-existing plan under the recipe's plan_code — even one whose billing
// config diverges from the recipe — is REUSED as-is (never conform-checked,
// never refused, never mutated). Instantiate SUCCEEDS, the recipe graph is
// built around the existing plan, and the response WARNS transparently
// (including that supplied base-fee params were not applied).
// Mutation-verify: restore ADR-083's conformance-refuse and this fails.
func TestService_Instantiate_ReusesExistingPlan(t *testing.T) {
	f := newRecipeFixture(t)
	ctx := postgres.WithLivemode(context.Background(), false)
	tenantID := testutil.CreateTestTenant(t, f.db, "reuse existing plan")

	// Operator has an in_advance $99 plan under the recipe's default plan_code
	// (the recipe declares in_arrears $0) — a divergence the recipe must NOT
	// reconcile.
	pre, err := f.pricingSvc.CreatePlan(ctx, tenantID, pricing.CreatePlanInput{
		Code: "ai_api_pro", Name: "Operator Pro", Currency: "USD",
		BillingInterval: domain.BillingMonthly, BaseAmountCents: 9900,
		BaseBillTiming: domain.BillInAdvance,
	})
	if err != nil {
		t.Fatalf("pre-create plan: %v", err)
	}

	// Supply base-fee params too — they can't be applied to an existing plan.
	inst, err := f.svc.Instantiate(ctx, tenantID, "anthropic_style",
		map[string]any{"base_amount_cents": 5000, "base_bill_timing": "in_arrears"}, InstantiateOptions{})
	if err != nil {
		t.Fatalf("expected reuse-and-succeed, got error: %v", err)
	}

	// The recipe graph WAS built (no refusal), around the existing plan.
	if n := countRows(t, f.db, tenantID, "recipe_instances"); n != 1 {
		t.Errorf("recipe_instances: got %d, want 1", n)
	}
	if n := countRows(t, f.db, tenantID, "rating_rule_versions"); n != 35 {
		t.Errorf("rating_rule_versions: got %d, want 35", n)
	}
	if n := countRows(t, f.db, tenantID, "plans"); n != 1 {
		t.Errorf("plans: got %d, want 1 (existing reused, not duplicated)", n)
	}
	// The existing plan is recorded as the recipe's plan, and is UNCHANGED.
	if len(inst.CreatedObjects.PlanIDs) != 1 || inst.CreatedObjects.PlanIDs[0] != pre.ID {
		t.Errorf("recipe should reuse existing plan id %s, got %v", pre.ID, inst.CreatedObjects.PlanIDs)
	}
	got, err := f.pricingSvc.GetPlan(ctx, tenantID, pre.ID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if got.BaseBillTiming != domain.BillInAdvance || got.BaseAmountCents != 9900 {
		t.Errorf("existing plan was mutated: timing=%s base=%d, want in_advance/9900 (never touched)", got.BaseBillTiming, got.BaseAmountCents)
	}
	// Transparency: reuse is not silent — a warning reports it AND that the
	// supplied params weren't applied.
	if len(inst.Warnings) == 0 {
		t.Fatal("expected a warning that the plan was reused as-is")
	}
	if !strings.Contains(inst.Warnings[0], "used as-is") || !strings.Contains(inst.Warnings[0], "NOT applied") {
		t.Errorf("warning should report reuse + params-not-applied, got %q", inst.Warnings[0])
	}
}

// TestService_Instantiate_OneCallInAdvancePlan is the ADR-084 one-call win: a
// fresh install seeds an in_advance $99 base plan directly via instantiate
// params — no follow-up PATCH.
func TestService_Instantiate_OneCallInAdvancePlan(t *testing.T) {
	f := newRecipeFixture(t)
	ctx := postgres.WithLivemode(context.Background(), false)
	tenantID := testutil.CreateTestTenant(t, f.db, "one-call in_advance")

	inst, err := f.svc.Instantiate(ctx, tenantID, "anthropic_style",
		map[string]any{"base_amount_cents": 9900, "base_bill_timing": "in_advance"}, InstantiateOptions{})
	if err != nil {
		t.Fatalf("Instantiate: %v", err)
	}
	if len(inst.CreatedObjects.PlanIDs) != 1 {
		t.Fatalf("expected 1 plan created, got %v", inst.CreatedObjects.PlanIDs)
	}
	got, err := f.pricingSvc.GetPlan(ctx, tenantID, inst.CreatedObjects.PlanIDs[0])
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if got.BaseAmountCents != 9900 || got.BaseBillTiming != domain.BillInAdvance {
		t.Errorf("one-call plan: got base=%d timing=%s, want 9900/in_advance", got.BaseAmountCents, got.BaseBillTiming)
	}
}

// TestService_Instantiate_RefusesDivergentMeter keeps the ADR-084 meter gate:
// a same-key meter whose aggregation diverges from the recipe (reference data
// that IS billing-consulted) is refused, and the tx rolls back.
func TestService_Instantiate_RefusesDivergentMeter(t *testing.T) {
	f := newRecipeFixture(t)
	ctx := postgres.WithLivemode(context.Background(), false)
	tenantID := testutil.CreateTestTenant(t, f.db, "divergent meter refuse")

	// Pre-existing `tokens` meter with a DIFFERENT aggregation than the recipe
	// declares (sum) — adopting it would silently mis-roll-up usage.
	if _, err := f.pricingSvc.CreateMeter(ctx, tenantID, pricing.CreateMeterInput{
		Key: "tokens", Name: "Tokens", Unit: "tokens", Aggregation: "max",
	}); err != nil {
		t.Fatalf("pre-create meter: %v", err)
	}

	_, err := f.svc.Instantiate(ctx, tenantID, "anthropic_style", nil, InstantiateOptions{})
	if !errors.Is(err, errs.ErrAlreadyExists) {
		t.Fatalf("expected refusal (ErrAlreadyExists) on divergent meter aggregation, got %v", err)
	}
	for _, tbl := range []string{"recipe_instances", "rating_rule_versions", "plans"} {
		if n := countRows(t, f.db, tenantID, tbl); n != 0 {
			t.Errorf("%s after refusal: got %d, want 0 (tx must roll back)", tbl, n)
		}
	}
	if n := countRows(t, f.db, tenantID, "meters"); n != 1 {
		t.Errorf("meters after refusal: got %d, want 1 (pre-existing, unchanged)", n)
	}
}
