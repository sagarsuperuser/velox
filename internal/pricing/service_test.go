package pricing

import (
	"context"
	"fmt"
	"testing"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
)

type memStore struct {
	rules  map[string]domain.RatingRuleVersion
	meters map[string]domain.Meter
	plans  map[string]domain.Plan
}

func newMemStore() *memStore {
	return &memStore{
		rules:  make(map[string]domain.RatingRuleVersion),
		meters: make(map[string]domain.Meter),
		plans:  make(map[string]domain.Plan),
	}
}

func (m *memStore) CreateRatingRule(_ context.Context, tenantID string, r domain.RatingRuleVersion) (domain.RatingRuleVersion, error) {
	for _, existing := range m.rules {
		if existing.TenantID == tenantID && existing.RuleKey == r.RuleKey && existing.Version == r.Version {
			return domain.RatingRuleVersion{}, fmt.Errorf("%w: rule_key %q version %d", errs.ErrAlreadyExists, r.RuleKey, r.Version)
		}
	}
	r.ID = fmt.Sprintf("vlx_rrv_%d", len(m.rules)+1)
	r.TenantID = tenantID
	m.rules[r.ID] = r
	return r, nil
}

func (m *memStore) GetRatingRule(_ context.Context, tenantID, id string) (domain.RatingRuleVersion, error) {
	r, ok := m.rules[id]
	if !ok || r.TenantID != tenantID {
		return domain.RatingRuleVersion{}, errs.ErrNotFound
	}
	return r, nil
}

func (m *memStore) ListRatingRules(_ context.Context, filter RatingRuleFilter) ([]domain.RatingRuleVersion, error) {
	var result []domain.RatingRuleVersion
	for _, r := range m.rules {
		if r.TenantID != filter.TenantID {
			continue
		}
		if filter.RuleKey != "" && r.RuleKey != filter.RuleKey {
			continue
		}
		if filter.LifecycleState != "" && string(r.LifecycleState) != filter.LifecycleState {
			continue
		}
		result = append(result, r)
	}
	return result, nil
}

func (m *memStore) CreateMeter(_ context.Context, tenantID string, meter domain.Meter) (domain.Meter, error) {
	for _, existing := range m.meters {
		if existing.TenantID == tenantID && existing.Key == meter.Key {
			return domain.Meter{}, fmt.Errorf("%w: meter key %q", errs.ErrAlreadyExists, meter.Key)
		}
	}
	meter.ID = fmt.Sprintf("vlx_mtr_%d", len(m.meters)+1)
	meter.TenantID = tenantID
	m.meters[meter.ID] = meter
	return meter, nil
}

func (m *memStore) GetMeter(_ context.Context, tenantID, id string) (domain.Meter, error) {
	meter, ok := m.meters[id]
	if !ok || meter.TenantID != tenantID {
		return domain.Meter{}, errs.ErrNotFound
	}
	return meter, nil
}

func (m *memStore) GetMeterByKey(_ context.Context, tenantID, key string) (domain.Meter, error) {
	for _, meter := range m.meters {
		if meter.TenantID == tenantID && meter.Key == key {
			return meter, nil
		}
	}
	return domain.Meter{}, errs.ErrNotFound
}

func (m *memStore) ListMeters(_ context.Context, tenantID string) ([]domain.Meter, error) {
	var result []domain.Meter
	for _, meter := range m.meters {
		if meter.TenantID == tenantID {
			result = append(result, meter)
		}
	}
	return result, nil
}

func (m *memStore) UpdateMeter(_ context.Context, tenantID string, meter domain.Meter) (domain.Meter, error) {
	existing, ok := m.meters[meter.ID]
	if !ok || existing.TenantID != tenantID {
		return domain.Meter{}, errs.ErrNotFound
	}
	m.meters[meter.ID] = meter
	return meter, nil
}

func (m *memStore) CreatePlan(_ context.Context, tenantID string, p domain.Plan) (domain.Plan, error) {
	for _, existing := range m.plans {
		if existing.TenantID == tenantID && existing.Code == p.Code {
			return domain.Plan{}, fmt.Errorf("%w: plan code %q", errs.ErrAlreadyExists, p.Code)
		}
	}
	p.ID = fmt.Sprintf("vlx_pln_%d", len(m.plans)+1)
	p.TenantID = tenantID
	m.plans[p.ID] = p
	return p, nil
}

func (m *memStore) GetPlan(_ context.Context, tenantID, id string) (domain.Plan, error) {
	p, ok := m.plans[id]
	if !ok || p.TenantID != tenantID {
		return domain.Plan{}, errs.ErrNotFound
	}
	return p, nil
}

func (m *memStore) ListPlans(_ context.Context, tenantID string) ([]domain.Plan, error) {
	var result []domain.Plan
	for _, p := range m.plans {
		if p.TenantID == tenantID {
			result = append(result, p)
		}
	}
	return result, nil
}

func (m *memStore) UpdatePlan(_ context.Context, tenantID string, p domain.Plan) (domain.Plan, error) {
	existing, ok := m.plans[p.ID]
	if !ok || existing.TenantID != tenantID {
		return domain.Plan{}, errs.ErrNotFound
	}
	m.plans[p.ID] = p
	return p, nil
}

func (m *memStore) CreateOverride(_ context.Context, tenantID string, o domain.CustomerPriceOverride) (domain.CustomerPriceOverride, error) {
	o.ID = fmt.Sprintf("vlx_cpo_%d", len(m.rules)+1)
	o.TenantID = tenantID
	o.Active = true
	return o, nil
}

func (m *memStore) GetOverride(_ context.Context, _, _, _ string) (domain.CustomerPriceOverride, error) {
	return domain.CustomerPriceOverride{}, errs.ErrNotFound
}

func (m *memStore) ListOverrides(_ context.Context, _, _ string) ([]domain.CustomerPriceOverride, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestCreateRatingRule_Flat(t *testing.T) {
	svc := NewService(newMemStore())
	ctx := context.Background()

	rule, err := svc.CreateRatingRule(ctx, "tenant1", CreateRatingRuleInput{
		RuleKey:         "api_calls",
		Name:            "API Calls",
		Mode:            domain.PricingFlat,
		Currency:        "usd",
		FlatAmountCents: 500,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rule.RuleKey != "api_calls" {
		t.Errorf("got rule_key %q, want %q", rule.RuleKey, "api_calls")
	}
	if rule.Version != 1 {
		t.Errorf("got version %d, want 1", rule.Version)
	}
	if rule.Currency != "USD" {
		t.Errorf("got currency %q, want USD", rule.Currency)
	}
	if rule.LifecycleState != domain.RatingRuleActive {
		t.Errorf("got lifecycle %q, want active", rule.LifecycleState)
	}
}

func TestCreateRatingRule_Graduated(t *testing.T) {
	svc := NewService(newMemStore())
	ctx := context.Background()

	rule, err := svc.CreateRatingRule(ctx, "tenant1", CreateRatingRuleInput{
		RuleKey:  "storage",
		Name:     "Storage GB",
		Mode:     domain.PricingGraduated,
		Currency: "USD",
		GraduatedTiers: []domain.RatingTier{
			{UpTo: 100, UnitAmountCents: 10},
			{UpTo: 0, UnitAmountCents: 5},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rule.GraduatedTiers) != 2 {
		t.Errorf("got %d tiers, want 2", len(rule.GraduatedTiers))
	}
}

func TestCreateRatingRule_Package(t *testing.T) {
	svc := NewService(newMemStore())
	ctx := context.Background()

	_, err := svc.CreateRatingRule(ctx, "tenant1", CreateRatingRuleInput{
		RuleKey:            "emails",
		Name:               "Emails",
		Mode:               domain.PricingPackage,
		Currency:           "USD",
		PackageSize:        1000,
		PackageAmountCents: 2000,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCreateRatingRule_AutoVersioning(t *testing.T) {
	svc := NewService(newMemStore())
	ctx := context.Background()

	v1, _ := svc.CreateRatingRule(ctx, "tenant1", CreateRatingRuleInput{
		RuleKey: "api_calls", Name: "V1", Mode: domain.PricingFlat, Currency: "USD", FlatAmountCents: 100,
	})
	v2, _ := svc.CreateRatingRule(ctx, "tenant1", CreateRatingRuleInput{
		RuleKey: "api_calls", Name: "V2", Mode: domain.PricingFlat, Currency: "USD", FlatAmountCents: 200,
	})

	if v1.Version != 1 {
		t.Errorf("v1 got version %d, want 1", v1.Version)
	}
	if v2.Version != 2 {
		t.Errorf("v2 got version %d, want 2", v2.Version)
	}
}

func TestCreateRatingRule_Validation(t *testing.T) {
	svc := NewService(newMemStore())
	ctx := context.Background()

	tests := []struct {
		name  string
		input CreateRatingRuleInput
	}{
		{"missing rule_key", CreateRatingRuleInput{Name: "test", Mode: domain.PricingFlat, Currency: "USD"}},
		{"missing name", CreateRatingRuleInput{RuleKey: "x", Mode: domain.PricingFlat, Currency: "USD"}},
		{"missing currency", CreateRatingRuleInput{RuleKey: "x", Name: "test", Mode: domain.PricingFlat}},
		{"invalid mode", CreateRatingRuleInput{RuleKey: "x", Name: "test", Mode: "bad", Currency: "USD"}},
		{"graduated no tiers", CreateRatingRuleInput{RuleKey: "x", Name: "test", Mode: domain.PricingGraduated, Currency: "USD"}},
		{"package zero size", CreateRatingRuleInput{RuleKey: "x", Name: "test", Mode: domain.PricingPackage, Currency: "USD", PackageSize: 0}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.CreateRatingRule(ctx, "tenant1", tt.input)
			if err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestCreateMeter(t *testing.T) {
	svc := NewService(newMemStore())
	ctx := context.Background()

	t.Run("valid", func(t *testing.T) {
		m, err := svc.CreateMeter(ctx, "tenant1", CreateMeterInput{
			Key:  "api_calls",
			Name: "API Calls",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if m.Key != "api_calls" {
			t.Errorf("got key %q, want %q", m.Key, "api_calls")
		}
		if m.Aggregation != "sum" {
			t.Errorf("got aggregation %q, want sum", m.Aggregation)
		}
	})

	t.Run("missing key", func(t *testing.T) {
		_, err := svc.CreateMeter(ctx, "tenant1", CreateMeterInput{Name: "test"})
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("invalid aggregation", func(t *testing.T) {
		_, err := svc.CreateMeter(ctx, "tenant1", CreateMeterInput{Key: "x", Name: "x", Aggregation: "invalid"})
		if err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestCreatePlan(t *testing.T) {
	svc := NewService(newMemStore())
	ctx := context.Background()

	t.Run("valid", func(t *testing.T) {
		p, err := svc.CreatePlan(ctx, "tenant1", CreatePlanInput{
			Code:            "pro",
			Name:            "Pro Plan",
			Currency:        "usd",
			BillingInterval: domain.BillingMonthly,
			BaseAmountCents: 9900,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if p.Code != "pro" {
			t.Errorf("got code %q, want %q", p.Code, "pro")
		}
		if p.Status != domain.PlanActive {
			t.Errorf("got status %q, want active", p.Status)
		}
		if p.Currency != "USD" {
			t.Errorf("got currency %q, want USD", p.Currency)
		}
	})

	t.Run("missing code", func(t *testing.T) {
		_, err := svc.CreatePlan(ctx, "tenant1", CreatePlanInput{Name: "x", Currency: "USD", BillingInterval: domain.BillingMonthly})
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("invalid billing interval", func(t *testing.T) {
		_, err := svc.CreatePlan(ctx, "tenant1", CreatePlanInput{Code: "x", Name: "x", Currency: "USD", BillingInterval: "weekly"})
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("duplicate code", func(t *testing.T) {
		_, _ = svc.CreatePlan(ctx, "tenant1", CreatePlanInput{Code: "dup", Name: "x", Currency: "USD", BillingInterval: domain.BillingMonthly})
		_, err := svc.CreatePlan(ctx, "tenant1", CreatePlanInput{Code: "dup", Name: "y", Currency: "USD", BillingInterval: domain.BillingMonthly})
		if err == nil {
			t.Fatal("expected error for duplicate code")
		}
	})
}

func TestUpdatePlan(t *testing.T) {
	svc := NewService(newMemStore())
	ctx := context.Background()

	created, _ := svc.CreatePlan(ctx, "tenant1", CreatePlanInput{
		Code: "basic", Name: "Basic", Currency: "USD", BillingInterval: domain.BillingMonthly, BaseAmountCents: 4900,
	})

	updated, err := svc.UpdatePlan(ctx, "tenant1", created.ID, CreatePlanInput{
		Name:            "Basic Plus",
		BaseAmountCents: 5900,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated.Name != "Basic Plus" {
		t.Errorf("got name %q, want %q", updated.Name, "Basic Plus")
	}
	if updated.BaseAmountCents != 5900 {
		t.Errorf("got base_amount %d, want 5900", updated.BaseAmountCents)
	}
}
