package recipe

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
)

// memStore is a non-tx in-memory fake of recipe.Store used by unit tests
// that exercise the registry-only methods (Preview, GetRecipe,
// ListRecipes). Tx-bearing paths (Instantiate, Uninstall) are covered by
// service_integration_test.go because they need a real *sql.Tx.
type memStore struct {
	byKey map[string]domain.RecipeInstance
}

func newMemStore() *memStore {
	return &memStore{byKey: make(map[string]domain.RecipeInstance)}
}

func (m *memStore) GetByKey(_ context.Context, _ string, recipeKey string) (domain.RecipeInstance, error) {
	inst, ok := m.byKey[recipeKey]
	if !ok {
		return domain.RecipeInstance{}, errs.ErrNotFound
	}
	return inst, nil
}

func (m *memStore) List(_ context.Context, _ string) ([]domain.RecipeInstance, error) {
	out := make([]domain.RecipeInstance, 0, len(m.byKey))
	for _, v := range m.byKey {
		out = append(out, v)
	}
	return out, nil
}

func (m *memStore) GetByID(_ context.Context, _, id string) (domain.RecipeInstance, error) {
	for _, v := range m.byKey {
		if v.ID == id {
			return v, nil
		}
	}
	return domain.RecipeInstance{}, errs.ErrNotFound
}

func (m *memStore) GetByKeyTx(_ context.Context, _ *sql.Tx, _ string, recipeKey string) (domain.RecipeInstance, error) {
	inst, ok := m.byKey[recipeKey]
	if !ok {
		return domain.RecipeInstance{}, errs.ErrNotFound
	}
	return inst, nil
}

func (m *memStore) CreateTx(_ context.Context, _ *sql.Tx, inst domain.RecipeInstance) (domain.RecipeInstance, error) {
	inst.ID = "vlx_rci_test_" + inst.RecipeKey
	m.byKey[inst.RecipeKey] = inst
	return inst, nil
}

func (m *memStore) DeleteByKeyTx(_ context.Context, _ *sql.Tx, _, recipeKey string) error {
	delete(m.byKey, recipeKey)
	return nil
}

func (m *memStore) DeleteByIDTx(_ context.Context, _ *sql.Tx, _, id string) error {
	for k, v := range m.byKey {
		if v.ID == id {
			delete(m.byKey, k)
			return nil
		}
	}
	return errs.ErrNotFound
}

// loadRegistry loads the embedded recipes for tests that need a real
// rendered recipe graph. Failing here means the bundled YAML is broken —
// surface it as a t.Fatal rather than masking with a nil registry.
func loadRegistry(t *testing.T) *Registry {
	t.Helper()
	reg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	return reg
}

func TestService_GetRecipe(t *testing.T) {
	svc := NewService(nil, newMemStore(), loadRegistry(t), nil, nil, nil)

	r, err := svc.GetRecipe("anthropic_style")
	if err != nil {
		t.Fatalf("GetRecipe: %v", err)
	}
	if r.Key != "anthropic_style" {
		t.Errorf("Key: got %q, want anthropic_style", r.Key)
	}
	if len(r.Overridable) == 0 {
		t.Error("expected overridable keys on anthropic_style")
	}

	if _, err := svc.GetRecipe("does_not_exist"); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestService_Preview_RendersTemplates(t *testing.T) {
	svc := NewService(nil, newMemStore(), loadRegistry(t), nil, nil, nil)

	rendered, err := svc.Preview(context.Background(), "anthropic_style", map[string]any{
		"currency":  "EUR",
		"plan_code": "ai_eur",
		"plan_name": "AI EUR",
	})
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if len(rendered.Plans) != 1 {
		t.Fatalf("expected 1 plan, got %d", len(rendered.Plans))
	}
	if got := rendered.Plans[0].Code; got != "ai_eur" {
		t.Errorf("plan.code: got %q, want ai_eur", got)
	}
	if got := rendered.Plans[0].Currency; got != "EUR" {
		t.Errorf("plan.currency: got %q, want EUR", got)
	}
	if got := rendered.RatingRules[0].Currency; got != "EUR" {
		t.Errorf("rating_rule.currency: got %q, want EUR", got)
	}
}

func TestService_Preview_UnknownRecipe(t *testing.T) {
	svc := NewService(nil, newMemStore(), loadRegistry(t), nil, nil, nil)
	if _, err := svc.Preview(context.Background(), "no_such_recipe", nil); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestService_Preview_RejectsBadOverride(t *testing.T) {
	svc := NewService(nil, newMemStore(), loadRegistry(t), nil, nil, nil)

	// "currency" enum is [USD, EUR, GBP]; "JPY" must fail.
	_, err := svc.Preview(context.Background(), "anthropic_style", map[string]any{
		"currency": "JPY",
	})
	if err == nil {
		t.Fatal("expected enum-violation error")
	}
	if !errors.Is(err, errs.ErrValidation) {
		t.Errorf("expected validation error, got %T (%v)", err, err)
	}
}

func TestService_ListRecipes_TagsInstalled(t *testing.T) {
	store := newMemStore()
	store.byKey["anthropic_style"] = domain.RecipeInstance{
		ID: "vlx_rci_existing", TenantID: "t1",
		RecipeKey: "anthropic_style", RecipeVersion: "1.0.0",
	}
	svc := NewService(nil, store, loadRegistry(t), nil, nil, nil)

	items, err := svc.ListRecipes(context.Background(), "t1")
	if err != nil {
		t.Fatalf("ListRecipes: %v", err)
	}
	if len(items) == 0 {
		t.Fatal("expected at least one recipe")
	}
	var found bool
	for _, item := range items {
		if item.Key == "anthropic_style" {
			found = true
			if item.Instantiated == nil {
				t.Error("expected Instantiated to be populated for installed recipe")
			} else if item.Instantiated.ID != "vlx_rci_existing" {
				t.Errorf("Instantiated.ID: got %q, want vlx_rci_existing", item.Instantiated.ID)
			}
		} else if item.Instantiated != nil {
			t.Errorf("recipe %q should be uninstalled but Instantiated is set", item.Key)
		}
	}
	if !found {
		t.Error("anthropic_style not in list")
	}
}

func TestService_Instantiate_ForceRejected(t *testing.T) {
	svc := NewService(nil, newMemStore(), loadRegistry(t), nil, nil, nil)

	_, err := svc.Instantiate(context.Background(), "t1", "anthropic_style", nil, InstantiateOptions{Force: true})
	if err == nil {
		t.Fatal("expected error when Force=true is requested in v1")
	}
	if !errors.Is(err, errs.ErrInvalidState) {
		t.Errorf("expected InvalidState, got %T (%v)", err, err)
	}
}

func TestService_Instantiate_UnknownRecipe(t *testing.T) {
	svc := NewService(nil, newMemStore(), loadRegistry(t), nil, nil, nil)

	_, err := svc.Instantiate(context.Background(), "t1", "no_such_recipe", nil, InstantiateOptions{})
	if !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestRatingRuleFromRecipe(t *testing.T) {
	got := ratingRuleFromRecipe(domain.RecipeRatingRule{
		Key: "tier1", Mode: domain.PricingFlat, Currency: "USD", FlatAmountCents: 100,
	})
	if got.RuleKey != "tier1" {
		t.Errorf("RuleKey: got %q", got.RuleKey)
	}
	if got.Name != "tier1" {
		t.Errorf("Name should default to Key: got %q", got.Name)
	}
	if got.Version != 1 {
		t.Errorf("Version: got %d, want 1", got.Version)
	}
	if got.LifecycleState != domain.RatingRuleActive {
		t.Errorf("LifecycleState: got %q, want active", got.LifecycleState)
	}
}

func TestDunningFromRecipe(t *testing.T) {
	got := dunningFromRecipe(domain.RecipeDunningPolicy{
		Name: "Test", MaxRetries: 4, IntervalsHours: []int{24, 72},
		FinalAction: "pause",
	})
	if !got.Enabled {
		t.Error("Enabled should be true")
	}
	if got.MaxRetryAttempts != 4 {
		t.Errorf("MaxRetryAttempts: got %d, want 4", got.MaxRetryAttempts)
	}
	if got.FinalAction != domain.DunningActionPause {
		t.Errorf("FinalAction: got %q, want pause", got.FinalAction)
	}
	if len(got.RetrySchedule) != 2 || got.RetrySchedule[0] != "24h" || got.RetrySchedule[1] != "72h" {
		t.Errorf("RetrySchedule: got %v, want [24h 72h]", got.RetrySchedule)
	}
	if got.GracePeriodDays != 3 {
		t.Errorf("GracePeriodDays default: got %d, want 3", got.GracePeriodDays)
	}
}

func TestDunningFromRecipe_DefaultsAction(t *testing.T) {
	got := dunningFromRecipe(domain.RecipeDunningPolicy{Name: "T", MaxRetries: 1})
	if got.FinalAction != domain.DunningActionManualReview {
		t.Errorf("default FinalAction: got %q, want manual_review", got.FinalAction)
	}
}
