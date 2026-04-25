package pricing

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
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
	if _, err := domain.ComputeAmountCents(rule, decimal.NewFromInt(1)); err != nil {
		return domain.RatingRuleVersion{}, errs.Invalid("pricing", fmt.Sprintf("invalid pricing configuration: %v", err))
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
		return errs.Required("rule_key")
	}
	if err := domain.MaxLen("rule_key", input.RuleKey, 100); err != nil {
		return err
	}
	if !slugPattern.MatchString(input.RuleKey) {
		return errs.Invalid("rule_key", "must contain only alphanumeric characters, hyphens, and underscores")
	}
	if strings.TrimSpace(input.Name) == "" {
		return errs.Required("name")
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
			return errs.Invalid("flat_amount_cents", "unit price must be greater than 0")
		}
	case domain.PricingGraduated:
		if len(input.GraduatedTiers) == 0 {
			return errs.Invalid("graduated_tiers", "at least one pricing tier is required")
		}
		for i, tier := range input.GraduatedTiers {
			if tier.UnitAmountCents <= 0 {
				return errs.Invalid("graduated_tiers", fmt.Sprintf("tier %d: unit price must be greater than 0", i+1))
			}
		}
	case domain.PricingPackage:
		if input.PackageSize <= 0 {
			return errs.Invalid("package_size", "package size must be greater than 0")
		}
		if input.PackageAmountCents <= 0 {
			return errs.Invalid("package_amount_cents", "package price must be greater than 0")
		}
	default:
		return errs.Invalid("mode", "must be one of: flat, graduated, package")
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
		return domain.Meter{}, errs.Required("key")
	}
	if err := domain.MaxLen("key", key, 100); err != nil {
		return domain.Meter{}, err
	}
	if !slugPattern.MatchString(key) {
		return domain.Meter{}, errs.Invalid("key", "must contain only alphanumeric characters, hyphens, and underscores")
	}
	if name == "" {
		return domain.Meter{}, errs.Required("name")
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
		return domain.Meter{}, errs.Invalid("aggregation", "must be one of: sum, count, max, last")
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
	TaxCode         string                 `json:"tax_code,omitempty"`
}

func (s *Service) CreatePlan(ctx context.Context, tenantID string, input CreatePlanInput) (domain.Plan, error) {
	code := strings.TrimSpace(input.Code)
	name := strings.TrimSpace(input.Name)
	currency := strings.ToUpper(strings.TrimSpace(input.Currency))

	if code == "" {
		return domain.Plan{}, errs.Required("code")
	}
	if err := domain.MaxLen("code", code, 100); err != nil {
		return domain.Plan{}, err
	}
	if !slugPattern.MatchString(code) {
		return domain.Plan{}, errs.Invalid("code", "must contain only alphanumeric characters, hyphens, and underscores")
	}
	if name == "" {
		return domain.Plan{}, errs.Required("name")
	}
	if err := domain.MaxLen("name", name, 255); err != nil {
		return domain.Plan{}, err
	}
	if err := domain.ValidateCurrency(currency); err != nil {
		return domain.Plan{}, err
	}
	if input.BillingInterval != domain.BillingMonthly && input.BillingInterval != domain.BillingYearly {
		return domain.Plan{}, errs.Invalid("billing_interval", "must be monthly or yearly")
	}
	if input.BaseAmountCents < 0 {
		return domain.Plan{}, errs.Invalid("base_amount_cents", "base fee must be 0 or more")
	}

	taxCode := strings.TrimSpace(input.TaxCode)
	if err := domain.ValidateStripeTaxCode("tax_code", taxCode); err != nil {
		return domain.Plan{}, err
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
		TaxCode:         taxCode,
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
	taxCode := strings.TrimSpace(input.TaxCode)
	if err := domain.ValidateStripeTaxCode("tax_code", taxCode); err != nil {
		return domain.Plan{}, err
	}
	existing.TaxCode = taxCode

	return s.store.UpdatePlan(ctx, tenantID, existing)
}

// ---------------------------------------------------------------------------
// Meter Pricing Rules — N-rules-per-meter dispatch.
// ---------------------------------------------------------------------------

// UpsertMeterPricingRuleInput is the public input shape. The combination
// (meter_id, rating_rule_version_id) identifies the rule — re-issuing
// the same point pair with new dimension_match / mode / priority
// updates the existing rule (idempotent reconfigure).
type UpsertMeterPricingRuleInput struct {
	MeterID             string                 `json:"meter_id"`
	RatingRuleVersionID string                 `json:"rating_rule_version_id"`
	DimensionMatch      map[string]any         `json:"dimension_match"`
	AggregationMode     domain.AggregationMode `json:"aggregation_mode"`
	Priority            int                    `json:"priority"`
}

// maxDimensionKeys caps the size of the JSONB filter to keep aggregation
// queries cheap and to bound pathological tenants. 16 dimensions is
// generous for the AI use case (model × operation × cached × tier ≈ 4),
// matches the open-question in the design doc, and is enforced here at
// the service boundary so the store never has to deal with bloated
// filters.
const maxDimensionKeys = 16

// UpsertMeterPricingRule validates the input and upserts the rule.
// Concretely the validations are:
//   - meter_id and rating_rule_version_id required
//   - rating rule must exist for this tenant (404 surfaces as 400 to
//     avoid leaking other tenants' IDs through the API surface)
//   - meter must exist for this tenant (same reasoning)
//   - aggregation_mode must be one of the five accepted values
//   - dimension_match has ≤ maxDimensionKeys keys
//   - dimension_match values are scalars (string / number / bool); object
//     and array values are rejected — Postgres `@>` would still match
//     them but the semantics aren't well-defined for v1
func (s *Service) UpsertMeterPricingRule(ctx context.Context, tenantID string, input UpsertMeterPricingRuleInput) (domain.MeterPricingRule, error) {
	meterID := strings.TrimSpace(input.MeterID)
	rrvID := strings.TrimSpace(input.RatingRuleVersionID)
	if meterID == "" {
		return domain.MeterPricingRule{}, errs.Required("meter_id")
	}
	if rrvID == "" {
		return domain.MeterPricingRule{}, errs.Required("rating_rule_version_id")
	}

	if _, err := s.store.GetMeter(ctx, tenantID, meterID); err != nil {
		return domain.MeterPricingRule{}, errs.Invalid("meter_id", fmt.Sprintf("meter %q not found", meterID))
	}
	if _, err := s.store.GetRatingRule(ctx, tenantID, rrvID); err != nil {
		return domain.MeterPricingRule{}, errs.Invalid("rating_rule_version_id", fmt.Sprintf("rating rule %q not found", rrvID))
	}

	mode := input.AggregationMode
	if mode == "" {
		mode = domain.AggSum
	}
	if !mode.IsValid() {
		return domain.MeterPricingRule{}, errs.Invalid("aggregation_mode", fmt.Sprintf("must be one of sum, count, last_during_period, last_ever, max; got %q", mode))
	}

	match := input.DimensionMatch
	if match == nil {
		match = map[string]any{}
	}
	if len(match) > maxDimensionKeys {
		return domain.MeterPricingRule{}, errs.Invalid("dimension_match", fmt.Sprintf("at most %d keys (got %d)", maxDimensionKeys, len(match)))
	}
	for k, v := range match {
		switch v.(type) {
		case string, bool, float64, float32, int, int32, int64, nil:
			// scalar — fine.
		default:
			return domain.MeterPricingRule{}, errs.Invalid("dimension_match", fmt.Sprintf("key %q value must be a scalar (string/number/bool), got %T", k, v))
		}
	}

	return s.store.UpsertMeterPricingRule(ctx, tenantID, domain.MeterPricingRule{
		MeterID:             meterID,
		RatingRuleVersionID: rrvID,
		DimensionMatch:      match,
		AggregationMode:     mode,
		Priority:            input.Priority,
	})
}

// GetMeterPricingRule fetches one rule by id.
func (s *Service) GetMeterPricingRule(ctx context.Context, tenantID, id string) (domain.MeterPricingRule, error) {
	return s.store.GetMeterPricingRule(ctx, tenantID, id)
}

// ListMeterPricingRulesByMeter returns rules in priority-DESC order; the
// store already enforces the ordering so callers can iterate top-down.
func (s *Service) ListMeterPricingRulesByMeter(ctx context.Context, tenantID, meterID string) ([]domain.MeterPricingRule, error) {
	if strings.TrimSpace(meterID) == "" {
		return nil, errs.Required("meter_id")
	}
	return s.store.ListMeterPricingRulesByMeter(ctx, tenantID, meterID)
}

// DeleteMeterPricingRule removes a rule. Pre-existing usage events are
// not retroactively re-scored; deletion only affects future billing
// finalize cycles.
func (s *Service) DeleteMeterPricingRule(ctx context.Context, tenantID, id string) error {
	return s.store.DeleteMeterPricingRule(ctx, tenantID, id)
}

// ---------------------------------------------------------------------------
// Tx variants — used by recipe.Service to compose pricing inserts inside a
// single cross-domain transaction. Validation is intentionally skipped here
// because the recipe template layer already validated the inputs against
// the recipe schema; re-validating in the recipe path would only duplicate
// what the template parser already enforced.
// ---------------------------------------------------------------------------

// CreateRatingRuleTx forwards to the store's tx-aware insert. Caller owns
// the *sql.Tx and is responsible for Commit/Rollback.
func (s *Service) CreateRatingRuleTx(ctx context.Context, tx *sql.Tx, tenantID string, rule domain.RatingRuleVersion) (domain.RatingRuleVersion, error) {
	return s.store.CreateRatingRuleTx(ctx, tx, tenantID, rule)
}

// CreateMeterTx forwards to the store's tx-aware insert.
func (s *Service) CreateMeterTx(ctx context.Context, tx *sql.Tx, tenantID string, m domain.Meter) (domain.Meter, error) {
	return s.store.CreateMeterTx(ctx, tx, tenantID, m)
}

// CreatePlanTx forwards to the store's tx-aware insert.
func (s *Service) CreatePlanTx(ctx context.Context, tx *sql.Tx, tenantID string, p domain.Plan) (domain.Plan, error) {
	return s.store.CreatePlanTx(ctx, tx, tenantID, p)
}

// UpsertMeterPricingRuleTx forwards to the store's tx-aware upsert.
func (s *Service) UpsertMeterPricingRuleTx(ctx context.Context, tx *sql.Tx, tenantID string, rule domain.MeterPricingRule) (domain.MeterPricingRule, error) {
	return s.store.UpsertMeterPricingRuleTx(ctx, tx, tenantID, rule)
}
