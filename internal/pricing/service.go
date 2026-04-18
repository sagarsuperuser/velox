package pricing

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/sagarsuperuser/velox/internal/domain"
)

var slugPattern = regexp.MustCompile(`^[a-zA-Z0-9_\-]+$`)

type Service struct {
	store Store
}

func NewService(store Store) *Service {
	return &Service{store: store}
}

// ---------------------------------------------------------------------------
// Rating Rules
// ---------------------------------------------------------------------------

type CreateRatingRuleInput struct {
	RuleKey                string              `json:"rule_key"`
	Name                   string              `json:"name"`
	Mode                   domain.PricingMode  `json:"mode"`
	Currency               string              `json:"currency"`
	FlatAmountCents        int64               `json:"flat_amount_cents"`
	GraduatedTiers         []domain.RatingTier `json:"graduated_tiers"`
	PackageSize            int64               `json:"package_size"`
	PackageAmountCents     int64               `json:"package_amount_cents"`
	OverageUnitAmountCents int64               `json:"overage_unit_amount_cents"`
}

func (s *Service) CreateRatingRule(ctx context.Context, tenantID string, input CreateRatingRuleInput) (domain.RatingRuleVersion, error) {
	if err := validateRatingRuleInput(input); err != nil {
		return domain.RatingRuleVersion{}, err
	}

	// Determine next version number
	existing, err := s.store.ListRatingRules(ctx, RatingRuleFilter{
		TenantID: tenantID,
		RuleKey:  input.RuleKey,
	})
	if err != nil {
		return domain.RatingRuleVersion{}, err
	}
	nextVersion := 1
	for _, r := range existing {
		if r.Version >= nextVersion {
			nextVersion = r.Version + 1
		}
	}

	rule := domain.RatingRuleVersion{
		RuleKey:                input.RuleKey,
		Name:                   input.Name,
		Version:                nextVersion,
		LifecycleState:         domain.RatingRuleActive,
		Mode:                   input.Mode,
		Currency:               strings.ToUpper(input.Currency),
		FlatAmountCents:        input.FlatAmountCents,
		GraduatedTiers:         input.GraduatedTiers,
		PackageSize:            input.PackageSize,
		PackageAmountCents:     input.PackageAmountCents,
		OverageUnitAmountCents: input.OverageUnitAmountCents,
	}

	// Validate the pricing config by computing a test amount
	if _, err := domain.ComputeAmountCents(rule, 1); err != nil {
		return domain.RatingRuleVersion{}, fmt.Errorf("invalid pricing configuration: %w", err)
	}

	return s.store.CreateRatingRule(ctx, tenantID, rule)
}

func (s *Service) GetRatingRule(ctx context.Context, tenantID, id string) (domain.RatingRuleVersion, error) {
	return s.store.GetRatingRule(ctx, tenantID, id)
}

// GetLatestRuleByKey returns the latest version of a rating rule by its key.
func (s *Service) GetLatestRuleByKey(ctx context.Context, tenantID, ruleKey string) (domain.RatingRuleVersion, error) {
	rules, err := s.store.ListRatingRules(ctx, RatingRuleFilter{
		TenantID:   tenantID,
		RuleKey:    ruleKey,
		LatestOnly: true,
	})
	if err != nil {
		return domain.RatingRuleVersion{}, err
	}
	if len(rules) == 0 {
		return domain.RatingRuleVersion{}, fmt.Errorf("no rating rule found for key %q", ruleKey)
	}
	return rules[0], nil
}

func (s *Service) ListRatingRules(ctx context.Context, filter RatingRuleFilter) ([]domain.RatingRuleVersion, error) {
	return s.store.ListRatingRules(ctx, filter)
}

func validateRatingRuleInput(input CreateRatingRuleInput) error {
	if strings.TrimSpace(input.RuleKey) == "" {
		return fmt.Errorf("key is required")
	}
	if err := domain.MaxLen("rule_key", input.RuleKey, 100); err != nil {
		return err
	}
	if !slugPattern.MatchString(input.RuleKey) {
		return fmt.Errorf("rule_key must contain only alphanumeric characters, hyphens, and underscores")
	}
	if strings.TrimSpace(input.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if err := domain.MaxLen("name", input.Name, 255); err != nil {
		return err
	}
	if err := domain.ValidateCurrency(input.Currency); err != nil {
		return err
	}

	switch input.Mode {
	case domain.PricingFlat:
		if input.FlatAmountCents <= 0 {
			return fmt.Errorf("unit price must be greater than 0")
		}
	case domain.PricingGraduated:
		if len(input.GraduatedTiers) == 0 {
			return fmt.Errorf("at least one pricing tier is required")
		}
		for i, tier := range input.GraduatedTiers {
			if tier.UnitAmountCents <= 0 {
				return fmt.Errorf("tier %d: unit price must be greater than 0", i+1)
			}
		}
	case domain.PricingPackage:
		if input.PackageSize <= 0 {
			return fmt.Errorf("package size must be greater than 0")
		}
		if input.PackageAmountCents <= 0 {
			return fmt.Errorf("package price must be greater than 0")
		}
	default:
		return fmt.Errorf("mode must be one of: flat, graduated, package")
	}

	return nil
}

// ---------------------------------------------------------------------------
// Meters
// ---------------------------------------------------------------------------

type CreateMeterInput struct {
	Key                 string `json:"key"`
	Name                string `json:"name"`
	Unit                string `json:"unit"`
	Aggregation         string `json:"aggregation"`
	RatingRuleVersionID string `json:"rating_rule_version_id,omitempty"`
}

func (s *Service) CreateMeter(ctx context.Context, tenantID string, input CreateMeterInput) (domain.Meter, error) {
	key := strings.TrimSpace(input.Key)
	name := strings.TrimSpace(input.Name)
	if key == "" {
		return domain.Meter{}, fmt.Errorf("key is required")
	}
	if err := domain.MaxLen("key", key, 100); err != nil {
		return domain.Meter{}, err
	}
	if !slugPattern.MatchString(key) {
		return domain.Meter{}, fmt.Errorf("key must contain only alphanumeric characters, hyphens, and underscores")
	}
	if name == "" {
		return domain.Meter{}, fmt.Errorf("name is required")
	}
	if err := domain.MaxLen("name", name, 255); err != nil {
		return domain.Meter{}, err
	}

	unit := input.Unit
	if unit == "" {
		unit = "unit"
	}
	agg := input.Aggregation
	if agg == "" {
		agg = "sum"
	}
	if agg != "sum" && agg != "count" && agg != "max" && agg != "last" {
		return domain.Meter{}, fmt.Errorf("aggregation must be one of: sum, count, max, last")
	}

	return s.store.CreateMeter(ctx, tenantID, domain.Meter{
		Key:                 key,
		Name:                name,
		Unit:                unit,
		Aggregation:         agg,
		RatingRuleVersionID: input.RatingRuleVersionID,
	})
}

func (s *Service) GetMeter(ctx context.Context, tenantID, id string) (domain.Meter, error) {
	return s.store.GetMeter(ctx, tenantID, id)
}

func (s *Service) GetMeterByKey(ctx context.Context, tenantID, key string) (domain.Meter, error) {
	return s.store.GetMeterByKey(ctx, tenantID, key)
}

func (s *Service) ListMeters(ctx context.Context, tenantID string) ([]domain.Meter, error) {
	return s.store.ListMeters(ctx, tenantID)
}

// ---------------------------------------------------------------------------
// Plans
// ---------------------------------------------------------------------------

type CreatePlanInput struct {
	Code            string                 `json:"code"`
	Name            string                 `json:"name"`
	Description     string                 `json:"description,omitempty"`
	Currency        string                 `json:"currency"`
	BillingInterval domain.BillingInterval `json:"billing_interval"`
	BaseAmountCents int64                  `json:"base_amount_cents"`
	MeterIDs        []string               `json:"meter_ids"`
	Status          string                 `json:"status,omitempty"`
}

func (s *Service) CreatePlan(ctx context.Context, tenantID string, input CreatePlanInput) (domain.Plan, error) {
	code := strings.TrimSpace(input.Code)
	name := strings.TrimSpace(input.Name)
	currency := strings.ToUpper(strings.TrimSpace(input.Currency))

	if code == "" {
		return domain.Plan{}, fmt.Errorf("code is required")
	}
	if err := domain.MaxLen("code", code, 100); err != nil {
		return domain.Plan{}, err
	}
	if !slugPattern.MatchString(code) {
		return domain.Plan{}, fmt.Errorf("code must contain only alphanumeric characters, hyphens, and underscores")
	}
	if name == "" {
		return domain.Plan{}, fmt.Errorf("name is required")
	}
	if err := domain.MaxLen("name", name, 255); err != nil {
		return domain.Plan{}, err
	}
	if err := domain.ValidateCurrency(currency); err != nil {
		return domain.Plan{}, err
	}
	if input.BillingInterval != domain.BillingMonthly && input.BillingInterval != domain.BillingYearly {
		return domain.Plan{}, fmt.Errorf("billing_interval must be monthly or yearly")
	}
	if input.BaseAmountCents < 0 {
		return domain.Plan{}, fmt.Errorf("base fee must be 0 or more")
	}

	if input.MeterIDs == nil {
		input.MeterIDs = []string{}
	}

	return s.store.CreatePlan(ctx, tenantID, domain.Plan{
		Code:            code,
		Name:            name,
		Description:     strings.TrimSpace(input.Description),
		Currency:        currency,
		BillingInterval: input.BillingInterval,
		Status:          domain.PlanActive,
		BaseAmountCents: input.BaseAmountCents,
		MeterIDs:        input.MeterIDs,
	})
}

func (s *Service) GetPlan(ctx context.Context, tenantID, id string) (domain.Plan, error) {
	return s.store.GetPlan(ctx, tenantID, id)
}

func (s *Service) ListPlans(ctx context.Context, tenantID string) ([]domain.Plan, error) {
	return s.store.ListPlans(ctx, tenantID)
}

func (s *Service) UpdatePlan(ctx context.Context, tenantID, id string, input CreatePlanInput) (domain.Plan, error) {
	existing, err := s.store.GetPlan(ctx, tenantID, id)
	if err != nil {
		return domain.Plan{}, err
	}

	if name := strings.TrimSpace(input.Name); name != "" {
		existing.Name = name
	}
	existing.Description = strings.TrimSpace(input.Description)
	if input.BaseAmountCents > 0 {
		existing.BaseAmountCents = input.BaseAmountCents
	}
	if input.MeterIDs != nil {
		existing.MeterIDs = input.MeterIDs
	}
	if input.Status != "" {
		existing.Status = domain.PlanStatus(input.Status)
	}

	return s.store.UpdatePlan(ctx, tenantID, existing)
}
