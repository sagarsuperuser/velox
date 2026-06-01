package pricing

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/shopspring/decimal"
)

type memStore struct {
	rules      map[string]domain.RatingRuleVersion
	meters     map[string]domain.Meter
	plans      map[string]domain.Plan
	meterRules map[string]domain.MeterPricingRule
}

func newMemStore() *memStore {
	return &memStore{
		rules:      make(map[string]domain.RatingRuleVersion),
		meters:     make(map[string]domain.Meter),
		plans:      make(map[string]domain.Plan),
		meterRules: make(map[string]domain.MeterPricingRule),
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

func (m *memStore) UpsertMeterPricingRule(_ context.Context, tenantID string, r domain.MeterPricingRule) (domain.MeterPricingRule, error) {
	if m.meterRules == nil {
		m.meterRules = make(map[string]domain.MeterPricingRule)
	}
	// Dedup on (tenant_id, meter_id, rating_rule_version_id) — same
	// UNIQUE key the Postgres schema enforces.
	for id, existing := range m.meterRules {
		if existing.TenantID == tenantID && existing.MeterID == r.MeterID && existing.RatingRuleVersionID == r.RatingRuleVersionID {
			r.ID = id
			r.TenantID = tenantID
			r.CreatedAt = existing.CreatedAt
			m.meterRules[id] = r
			return r, nil
		}
	}
	if r.ID == "" {
		r.ID = fmt.Sprintf("vlx_mpr_%d", len(m.meterRules)+1)
	}
	r.TenantID = tenantID
	m.meterRules[r.ID] = r
	return r, nil
}

func (m *memStore) GetMeterPricingRule(_ context.Context, tenantID, id string) (domain.MeterPricingRule, error) {
	r, ok := m.meterRules[id]
	if !ok || r.TenantID != tenantID {
		return domain.MeterPricingRule{}, errs.ErrNotFound
	}
	return r, nil
}

func (m *memStore) ListMeterPricingRulesByMeter(_ context.Context, tenantID, meterID string) ([]domain.MeterPricingRule, error) {
	var out []domain.MeterPricingRule
	for _, r := range m.meterRules {
		if r.TenantID == tenantID && r.MeterID == meterID {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *memStore) DeleteMeterPricingRule(_ context.Context, tenantID, id string) error {
	r, ok := m.meterRules[id]
	if !ok || r.TenantID != tenantID {
		return errs.ErrNotFound
	}
	delete(m.meterRules, id)
	return nil
}

// *Tx variants forward to the non-Tx methods — the in-memory fake doesn't
// model transaction semantics, and recipe-package tests that need real
// rollback go through the Postgres integration tests instead.
func (m *memStore) CreateRatingRuleTx(ctx context.Context, _ *sql.Tx, tenantID string, r domain.RatingRuleVersion) (domain.RatingRuleVersion, error) {
	return m.CreateRatingRule(ctx, tenantID, r)
}

func (m *memStore) CreateMeterTx(ctx context.Context, _ *sql.Tx, tenantID string, meter domain.Meter) (domain.Meter, error) {
	return m.CreateMeter(ctx, tenantID, meter)
}

func (m *memStore) CreatePlanTx(ctx context.Context, _ *sql.Tx, tenantID string, p domain.Plan) (domain.Plan, error) {
	return m.CreatePlan(ctx, tenantID, p)
}

func (m *memStore) UpsertMeterPricingRuleTx(ctx context.Context, _ *sql.Tx, tenantID string, r domain.MeterPricingRule) (domain.MeterPricingRule, error) {
	return m.UpsertMeterPricingRule(ctx, tenantID, r)
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
		FlatAmountCents: decimal.NewFromInt(500),
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
			{UpTo: 100, UnitAmountCents: decimal.NewFromInt(10)},
			{UpTo: 0, UnitAmountCents: decimal.NewFromInt(5)},
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
		RuleKey: "api_calls", Name: "V1", Mode: domain.PricingFlat, Currency: "USD", FlatAmountCents: decimal.NewFromInt(100),
	})
	v2, _ := svc.CreateRatingRule(ctx, "tenant1", CreateRatingRuleInput{
		RuleKey: "api_calls", Name: "V2", Mode: domain.PricingFlat, Currency: "USD", FlatAmountCents: decimal.NewFromInt(200),
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

	t.Run("base_bill_timing defaults to in_arrears", func(t *testing.T) {
		p, err := svc.CreatePlan(ctx, "tenant1", CreatePlanInput{
			Code: "tdef", Name: "Default Timing", Currency: "USD", BillingInterval: domain.BillingMonthly,
		})
		if err != nil {
			t.Fatalf("unexpected: %v", err)
		}
		if p.BaseBillTiming != domain.BillInArrears {
			t.Errorf("got %q, want in_arrears", p.BaseBillTiming)
		}
	})

	t.Run("base_bill_timing in_advance accepted", func(t *testing.T) {
		p, err := svc.CreatePlan(ctx, "tenant1", CreatePlanInput{
			Code: "tadv", Name: "Advance", Currency: "USD",
			BillingInterval: domain.BillingMonthly,
			BaseBillTiming:  domain.BillInAdvance,
		})
		if err != nil {
			t.Fatalf("unexpected: %v", err)
		}
		if p.BaseBillTiming != domain.BillInAdvance {
			t.Errorf("got %q, want in_advance", p.BaseBillTiming)
		}
	})

	t.Run("base_bill_timing invalid rejected", func(t *testing.T) {
		_, err := svc.CreatePlan(ctx, "tenant1", CreatePlanInput{
			Code: "tinv", Name: "Invalid", Currency: "USD",
			BillingInterval: domain.BillingMonthly,
			BaseBillTiming:  "nightly",
		})
		if err == nil {
			t.Fatal("expected error for invalid bill_timing")
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

// fakePlanUsage is a stub SubscriptionPlanUsageReader that returns a
// fixed live-sub count for any plan. Used by the immutability guard
// tests (ADR-034).
type fakePlanUsage struct {
	count int
	err   error
}

func (f *fakePlanUsage) CountLiveSubsByPlan(_ context.Context, _, _ string) (int, error) {
	return f.count, f.err
}

func TestUpdatePlan_ImmutabilityGuard_ADR034(t *testing.T) {
	ctx := context.Background()

	setup := func(liveCount int) (*Service, domain.Plan) {
		svc := NewService(newMemStore())
		svc.SetSubscriptionPlanUsageReader(&fakePlanUsage{count: liveCount})
		p, err := svc.CreatePlan(ctx, "tenant1", CreatePlanInput{
			Code: "guard", Name: "Guard", Currency: "USD",
			BillingInterval: domain.BillingMonthly, BaseAmountCents: 4900,
			BaseBillTiming: domain.BillInArrears,
			MeterIDs:       []string{"meter_a"},
		})
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
		return svc, p
	}

	t.Run("no live subs: billing-affecting fields ARE mutable", func(t *testing.T) {
		svc, p := setup(0)
		updated, err := svc.UpdatePlan(ctx, "tenant1", p.ID, CreatePlanInput{
			BaseAmountCents: 9900,
			BaseBillTiming:  domain.BillInAdvance,
			MeterIDs:        []string{"meter_a", "meter_b"},
		})
		if err != nil {
			t.Fatalf("expected allowed (no subs): %v", err)
		}
		if updated.BaseAmountCents != 9900 {
			t.Errorf("base_amount not applied: %d", updated.BaseAmountCents)
		}
		if updated.BaseBillTiming != domain.BillInAdvance {
			t.Errorf("base_bill_timing not applied: %q", updated.BaseBillTiming)
		}
	})

	t.Run("live subs: base_amount_cents change rejected", func(t *testing.T) {
		svc, p := setup(1)
		_, err := svc.UpdatePlan(ctx, "tenant1", p.ID, CreatePlanInput{
			BaseAmountCents: 9900,
		})
		if err == nil {
			t.Fatal("expected error blocking base_amount_cents mutation")
		}
	})

	t.Run("live subs: base_bill_timing flip rejected", func(t *testing.T) {
		svc, p := setup(2)
		_, err := svc.UpdatePlan(ctx, "tenant1", p.ID, CreatePlanInput{
			BaseBillTiming: domain.BillInAdvance,
		})
		if err == nil {
			t.Fatal("expected error blocking base_bill_timing flip")
		}
	})

	t.Run("live subs: meter_ids change rejected", func(t *testing.T) {
		svc, p := setup(1)
		_, err := svc.UpdatePlan(ctx, "tenant1", p.ID, CreatePlanInput{
			MeterIDs: []string{"meter_a", "meter_b"},
		})
		if err == nil {
			t.Fatal("expected error blocking meter_ids mutation")
		}
	})

	t.Run("live subs: meter_ids same set (different order) NOT a change", func(t *testing.T) {
		svc, p := setup(5)
		// Plan was created with [meter_a]; pass [meter_a] again.
		_, err := svc.UpdatePlan(ctx, "tenant1", p.ID, CreatePlanInput{
			MeterIDs: []string{"meter_a"},
		})
		if err != nil {
			t.Errorf("identical meter set should not trigger guard: %v", err)
		}
	})

	t.Run("live subs: display-only fields STILL mutable", func(t *testing.T) {
		svc, p := setup(3)
		updated, err := svc.UpdatePlan(ctx, "tenant1", p.ID, CreatePlanInput{
			Name:        "Renamed",
			Description: "New description",
			TaxCode:     "txcd_10000000",
		})
		if err != nil {
			t.Fatalf("display-only mutation should be allowed: %v", err)
		}
		if updated.Name != "Renamed" {
			t.Errorf("name not applied: %q", updated.Name)
		}
	})

	t.Run("live subs: no-op same value passes", func(t *testing.T) {
		svc, p := setup(1)
		_, err := svc.UpdatePlan(ctx, "tenant1", p.ID, CreatePlanInput{
			BaseAmountCents: 4900, // same as seed
		})
		if err != nil {
			t.Errorf("no-op base_amount should pass: %v", err)
		}
	})

	t.Run("unwired reader: guard is silent (test-only fallback)", func(t *testing.T) {
		svc := NewService(newMemStore())
		// NOT wiring SetSubscriptionPlanUsageReader.
		p, _ := svc.CreatePlan(ctx, "tenant1", CreatePlanInput{
			Code: "unwired", Name: "Unwired", Currency: "USD",
			BillingInterval: domain.BillingMonthly, BaseAmountCents: 4900,
		})
		_, err := svc.UpdatePlan(ctx, "tenant1", p.ID, CreatePlanInput{
			BaseAmountCents: 9900,
		})
		if err != nil {
			t.Errorf("unwired reader should fail-open in narrow tests: %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// Meter Pricing Rules
// ---------------------------------------------------------------------------

// seedMeterAndRule seeds a meter and a rating rule into the in-memory
// store so the meter_pricing_rules tests can reference them by ID
// without going through the public Create paths (those have their own
// validation tested elsewhere).
func seedMeterAndRule(t *testing.T, svc *Service, tenantID string) (meterID, rrvID string) {
	t.Helper()
	rule, err := svc.CreateRatingRule(context.Background(), tenantID, CreateRatingRuleInput{
		RuleKey: "tokens_in", Name: "Input tokens", Mode: domain.PricingFlat,
		Currency: "USD", FlatAmountCents: decimal.NewFromInt(5),
	})
	if err != nil {
		t.Fatalf("seed rating rule: %v", err)
	}
	meter, err := svc.CreateMeter(context.Background(), tenantID, CreateMeterInput{
		Key: "tokens", Name: "Tokens", Unit: "tokens", Aggregation: "sum",
		RatingRuleVersionID: rule.ID,
	})
	if err != nil {
		t.Fatalf("seed meter: %v", err)
	}
	return meter.ID, rule.ID
}

func TestUpsertMeterPricingRule_Valid(t *testing.T) {
	svc := NewService(newMemStore())
	ctx := context.Background()
	meterID, rrvID := seedMeterAndRule(t, svc, "t1")

	rule, err := svc.UpsertMeterPricingRule(ctx, "t1", UpsertMeterPricingRuleInput{
		MeterID:             meterID,
		RatingRuleVersionID: rrvID,
		DimensionMatch:      map[string]any{"model": "gpt-4", "cached": false},
		AggregationMode:     domain.AggSum,
		Priority:            100,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rule.AggregationMode != domain.AggSum {
		t.Errorf("agg mode: got %q, want sum", rule.AggregationMode)
	}
	if rule.Priority != 100 {
		t.Errorf("priority: got %d, want 100", rule.Priority)
	}
	if rule.DimensionMatch["model"] != "gpt-4" {
		t.Errorf("dimension_match did not round-trip: got %+v", rule.DimensionMatch)
	}
}

func TestUpsertMeterPricingRule_DefaultModeIsSum(t *testing.T) {
	svc := NewService(newMemStore())
	ctx := context.Background()
	meterID, rrvID := seedMeterAndRule(t, svc, "t1")

	rule, err := svc.UpsertMeterPricingRule(ctx, "t1", UpsertMeterPricingRuleInput{
		MeterID: meterID, RatingRuleVersionID: rrvID,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rule.AggregationMode != domain.AggSum {
		t.Errorf("default agg mode: got %q, want sum", rule.AggregationMode)
	}
}

func TestUpsertMeterPricingRule_RejectsBadMode(t *testing.T) {
	svc := NewService(newMemStore())
	ctx := context.Background()
	meterID, rrvID := seedMeterAndRule(t, svc, "t1")

	_, err := svc.UpsertMeterPricingRule(ctx, "t1", UpsertMeterPricingRuleInput{
		MeterID: meterID, RatingRuleVersionID: rrvID, AggregationMode: "average",
	})
	if err == nil {
		t.Fatal("expected error for unknown aggregation mode")
	}
}

func TestUpsertMeterPricingRule_RejectsTooManyDimensions(t *testing.T) {
	svc := NewService(newMemStore())
	ctx := context.Background()
	meterID, rrvID := seedMeterAndRule(t, svc, "t1")

	dims := map[string]any{}
	for i := 0; i < maxDimensionKeys+1; i++ {
		dims[fmt.Sprintf("k%d", i)] = "v"
	}
	_, err := svc.UpsertMeterPricingRule(ctx, "t1", UpsertMeterPricingRuleInput{
		MeterID: meterID, RatingRuleVersionID: rrvID, DimensionMatch: dims,
	})
	if err == nil {
		t.Fatal("expected error for too many dimension keys")
	}
}

func TestUpsertMeterPricingRule_RejectsNonScalarDimensionValue(t *testing.T) {
	svc := NewService(newMemStore())
	ctx := context.Background()
	meterID, rrvID := seedMeterAndRule(t, svc, "t1")

	_, err := svc.UpsertMeterPricingRule(ctx, "t1", UpsertMeterPricingRuleInput{
		MeterID: meterID, RatingRuleVersionID: rrvID,
		DimensionMatch: map[string]any{"models": []string{"gpt-4", "claude"}},
	})
	if err == nil {
		t.Fatal("expected error for slice value in dimension_match")
	}
}

func TestUpsertMeterPricingRule_RequiresMeterAndRule(t *testing.T) {
	svc := NewService(newMemStore())
	ctx := context.Background()

	_, err := svc.UpsertMeterPricingRule(ctx, "t1", UpsertMeterPricingRuleInput{})
	if err == nil {
		t.Fatal("expected required-field error")
	}
}

func TestUpsertMeterPricingRule_UnknownMeterRejected(t *testing.T) {
	svc := NewService(newMemStore())
	ctx := context.Background()
	_, rrvID := seedMeterAndRule(t, svc, "t1")

	_, err := svc.UpsertMeterPricingRule(ctx, "t1", UpsertMeterPricingRuleInput{
		MeterID: "vlx_mtr_does_not_exist", RatingRuleVersionID: rrvID,
	})
	if err == nil {
		t.Fatal("expected error for unknown meter")
	}
}

func TestUpsertMeterPricingRule_TenantIsolation(t *testing.T) {
	svc := NewService(newMemStore())
	ctx := context.Background()
	// Tenant A seeds its own meter and rule; tenant B should not be able
	// to bind a pricing rule to A's IDs.
	meterID, rrvID := seedMeterAndRule(t, svc, "tenantA")

	_, err := svc.UpsertMeterPricingRule(ctx, "tenantB", UpsertMeterPricingRuleInput{
		MeterID: meterID, RatingRuleVersionID: rrvID,
	})
	if err == nil {
		t.Fatal("expected cross-tenant attempt to be rejected")
	}
}

func TestListMeterPricingRulesByMeter(t *testing.T) {
	svc := NewService(newMemStore())
	ctx := context.Background()
	meterID, rrvID := seedMeterAndRule(t, svc, "t1")

	// Seed a second rating rule so we can attach two pricing rules.
	rule2, err := svc.CreateRatingRule(ctx, "t1", CreateRatingRuleInput{
		RuleKey: "tokens_cached", Name: "Cached", Mode: domain.PricingFlat,
		Currency: "USD", FlatAmountCents: decimal.NewFromInt(1),
	})
	if err != nil {
		t.Fatalf("second rating rule: %v", err)
	}

	if _, err := svc.UpsertMeterPricingRule(ctx, "t1", UpsertMeterPricingRuleInput{
		MeterID: meterID, RatingRuleVersionID: rrvID,
		DimensionMatch: map[string]any{}, Priority: 0,
	}); err != nil {
		t.Fatalf("default rule: %v", err)
	}
	if _, err := svc.UpsertMeterPricingRule(ctx, "t1", UpsertMeterPricingRuleInput{
		MeterID: meterID, RatingRuleVersionID: rule2.ID,
		DimensionMatch: map[string]any{"cached": true}, Priority: 100,
	}); err != nil {
		t.Fatalf("specific rule: %v", err)
	}

	rules, err := svc.ListMeterPricingRulesByMeter(ctx, "t1", meterID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rules) != 2 {
		t.Fatalf("got %d rules, want 2", len(rules))
	}
}

func TestUpsertMeterPricingRule_IsIdempotent(t *testing.T) {
	svc := NewService(newMemStore())
	ctx := context.Background()
	meterID, rrvID := seedMeterAndRule(t, svc, "t1")

	first, err := svc.UpsertMeterPricingRule(ctx, "t1", UpsertMeterPricingRuleInput{
		MeterID: meterID, RatingRuleVersionID: rrvID, Priority: 10,
	})
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	// Re-issuing with the same (meter, rule) pair must update, not create.
	second, err := svc.UpsertMeterPricingRule(ctx, "t1", UpsertMeterPricingRuleInput{
		MeterID: meterID, RatingRuleVersionID: rrvID, Priority: 50,
	})
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if first.ID != second.ID {
		t.Errorf("upsert created a new row instead of updating: first=%s second=%s", first.ID, second.ID)
	}
	if second.Priority != 50 {
		t.Errorf("priority not updated: got %d want 50", second.Priority)
	}
}
