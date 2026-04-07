package pricing_test

import (
	"context"
	"testing"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/pricing"
	"github.com/sagarsuperuser/velox/internal/testutil"
)

func TestPostgresStore_RatingRules(t *testing.T) {
	db := testutil.SetupTestDB(t)
	store := pricing.NewPostgresStore(db)
	ctx := context.Background()
	tenantID := testutil.CreateTestTenant(t, db, "Test")

	// Create flat rule
	rule, err := store.CreateRatingRule(ctx, tenantID, domain.RatingRuleVersion{
		RuleKey: "api_calls", Name: "API Calls", Version: 1,
		LifecycleState: domain.RatingRuleDraft, Mode: domain.PricingFlat,
		Currency: "USD", FlatAmountCents: 500,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if rule.ID == "" {
		t.Fatal("ID should be generated")
	}

	// Get
	got, err := store.GetRatingRule(ctx, tenantID, rule.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.FlatAmountCents != 500 {
		t.Errorf("flat_amount: got %d", got.FlatAmountCents)
	}

	// Create graduated rule with tiers
	grad, err := store.CreateRatingRule(ctx, tenantID, domain.RatingRuleVersion{
		RuleKey: "storage", Name: "Storage", Version: 1,
		LifecycleState: domain.RatingRuleActive, Mode: domain.PricingGraduated,
		Currency: "USD",
		GraduatedTiers: []domain.RatingTier{
			{UpTo: 100, UnitAmountCents: 10},
			{UpTo: 0, UnitAmountCents: 5},
		},
	})
	if err != nil {
		t.Fatalf("create graduated: %v", err)
	}

	// Verify tiers persisted
	gotGrad, _ := store.GetRatingRule(ctx, tenantID, grad.ID)
	if len(gotGrad.GraduatedTiers) != 2 {
		t.Fatalf("tiers: got %d, want 2", len(gotGrad.GraduatedTiers))
	}
	if gotGrad.GraduatedTiers[0].UpTo != 100 {
		t.Errorf("tier[0].up_to: got %d", gotGrad.GraduatedTiers[0].UpTo)
	}

	// List
	all, err := store.ListRatingRules(ctx, pricing.RatingRuleFilter{TenantID: tenantID})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("list count: got %d, want 2", len(all))
	}

	// Unique constraint
	_, err = store.CreateRatingRule(ctx, tenantID, domain.RatingRuleVersion{
		RuleKey: "api_calls", Name: "Dup", Version: 1,
		LifecycleState: domain.RatingRuleDraft, Mode: domain.PricingFlat,
		Currency: "USD",
	})
	if err == nil {
		t.Fatal("expected unique violation for duplicate rule_key+version")
	}
}

func TestPostgresStore_Meters(t *testing.T) {
	db := testutil.SetupTestDB(t)
	store := pricing.NewPostgresStore(db)
	ctx := context.Background()
	tenantID := testutil.CreateTestTenant(t, db, "Test")

	meter, err := store.CreateMeter(ctx, tenantID, domain.Meter{
		Key: "api_calls", Name: "API Calls", Unit: "calls", Aggregation: "sum",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := store.GetMeter(ctx, tenantID, meter.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Key != "api_calls" {
		t.Errorf("key: got %q", got.Key)
	}

	// Unique key per tenant
	_, err = store.CreateMeter(ctx, tenantID, domain.Meter{
		Key: "api_calls", Name: "Dup", Unit: "x", Aggregation: "sum",
	})
	if err == nil {
		t.Fatal("expected unique violation for duplicate meter key")
	}

	// List
	meters, err := store.ListMeters(ctx, tenantID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(meters) != 1 {
		t.Errorf("list: got %d, want 1", len(meters))
	}

	// RLS: different tenant can't see it
	tenant2 := testutil.CreateTestTenant(t, db, "Other")
	_, err = store.GetMeter(ctx, tenant2, meter.ID)
	if err != errs.ErrNotFound {
		t.Errorf("RLS: tenant2 should not see tenant1 meter, got %v", err)
	}
}

func TestPostgresStore_Plans(t *testing.T) {
	db := testutil.SetupTestDB(t)
	store := pricing.NewPostgresStore(db)
	ctx := context.Background()
	tenantID := testutil.CreateTestTenant(t, db, "Test")

	plan, err := store.CreatePlan(ctx, tenantID, domain.Plan{
		Code: "pro", Name: "Pro Plan", Currency: "USD",
		BillingInterval: domain.BillingMonthly, Status: domain.PlanDraft,
		BaseAmountCents: 4900, MeterIDs: []string{"mtr_1", "mtr_2"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := store.GetPlan(ctx, tenantID, plan.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Code != "pro" {
		t.Errorf("code: got %q", got.Code)
	}
	if got.BaseAmountCents != 4900 {
		t.Errorf("base_amount: got %d", got.BaseAmountCents)
	}
	if len(got.MeterIDs) != 2 {
		t.Errorf("meter_ids: got %d, want 2", len(got.MeterIDs))
	}

	// Update
	got.Name = "Pro Plus"
	got.BaseAmountCents = 5900
	updated, err := store.UpdatePlan(ctx, tenantID, got)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Name != "Pro Plus" {
		t.Errorf("updated name: got %q", updated.Name)
	}
	if updated.BaseAmountCents != 5900 {
		t.Errorf("updated base: got %d", updated.BaseAmountCents)
	}
}
