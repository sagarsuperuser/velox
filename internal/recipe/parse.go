package recipe

import (
	"fmt"
	"regexp"

	"gopkg.in/yaml.v3"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// rawRecipe is the shape recipes/*.yaml deserializes into. It mirrors the
// YAML field names; parseRecipe converts it to the canonical domain.Recipe.
// Keeping the wire form separate keeps domain.Recipe free of YAML tags and
// lets us evolve the YAML schema without leaking transient parse fields
// into the rest of the codebase.
type rawRecipe struct {
	Key         string                  `yaml:"key"`
	Version     string                  `yaml:"version"`
	Name        string                  `yaml:"name"`
	Summary     string                  `yaml:"summary"`
	Description string                  `yaml:"description"`
	Overridable []rawOverride           `yaml:"overridable"`
	Products    []rawProduct            `yaml:"products"`
	Meters      []rawMeter              `yaml:"meters"`
	RatingRules []rawRatingRule         `yaml:"rating_rules"`
	PricingRules []rawPricingRule       `yaml:"pricing_rules"`
	Plans       []rawPlan               `yaml:"plans"`
	Dunning     *rawDunningSection      `yaml:"dunning"`
	Webhook     *rawWebhook             `yaml:"webhook"`
	SampleData  *rawSampleData          `yaml:"sample_data"`
}

type rawOverride struct {
	Key       string   `yaml:"key"`
	Type      string   `yaml:"type"`
	Default   any      `yaml:"default"`
	Enum      []string `yaml:"enum"`
	MaxLength int      `yaml:"max_length"`
	Pattern   string   `yaml:"pattern"`
}

type rawProduct struct {
	Code        string `yaml:"code"`
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

type rawMeter struct {
	Key         string `yaml:"key"`
	Name        string `yaml:"name"`
	Unit        string `yaml:"unit"`
	Aggregation string `yaml:"aggregation"`
}

type rawRatingTier struct {
	UpTo            int64 `yaml:"up_to"`
	UnitAmountCents int64 `yaml:"unit_amount_cents"`
}

type rawRatingRule struct {
	Key                    string          `yaml:"key"`
	Name                   string          `yaml:"name"`
	Mode                   string          `yaml:"mode"`
	Currency               string          `yaml:"currency"`
	FlatAmountCents        int64           `yaml:"flat_amount_cents"`
	GraduatedTiers         []rawRatingTier `yaml:"graduated_tiers"`
	PackageSize            int64           `yaml:"package_size"`
	PackageAmountCents     int64           `yaml:"package_amount_cents"`
	OverageUnitAmountCents int64           `yaml:"overage_unit_amount_cents"`
}

type rawPricingRule struct {
	Meter           string         `yaml:"meter"`
	RatingRule      string         `yaml:"rating_rule"`
	DimensionMatch  map[string]any `yaml:"dimension_match"`
	AggregationMode string         `yaml:"aggregation_mode"`
	Priority        int            `yaml:"priority"`
}

type rawPlan struct {
	Code            string   `yaml:"code"`
	Name            string   `yaml:"name"`
	Currency        string   `yaml:"currency"`
	BillingInterval string   `yaml:"billing_interval"`
	BaseAmountCents int64    `yaml:"base_amount_cents"`
	Meters          []string `yaml:"meters"`
}

type rawDunningSection struct {
	Policy rawDunningPolicy `yaml:"policy"`
}

type rawDunningPolicy struct {
	Name           string `yaml:"name"`
	MaxRetries     int    `yaml:"max_retries"`
	IntervalsHours []int  `yaml:"intervals_hours"`
	FinalAction    string `yaml:"final_action"`
}

type rawWebhook struct {
	Events         []string `yaml:"events"`
	URLPlaceholder string   `yaml:"url_placeholder"`
}

type rawSampleData struct {
	Customer     rawSampleCustomer     `yaml:"customer"`
	Subscription rawSampleSubscription `yaml:"subscription"`
}

type rawSampleCustomer struct {
	ExternalID  string `yaml:"external_id"`
	DisplayName string `yaml:"display_name"`
	Email       string `yaml:"email"`
}

type rawSampleSubscription struct {
	Plan      string `yaml:"plan"`
	TrialDays int    `yaml:"trial_days"`
}

// recipeKeyPattern bounds keys to lowercase identifiers so they can ride
// through URLs (`/v1/recipes/{key}`) without escaping.
var recipeKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// parseRecipe reads YAML bytes, validates the structural invariants we
// rely on at runtime, and returns the canonical domain.Recipe. Errors are
// wrapped with the offending field name so a malformed recipe in CI
// surfaces the exact line at boot.
func parseRecipe(data []byte) (domain.Recipe, error) {
	var raw rawRecipe
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return domain.Recipe{}, fmt.Errorf("yaml unmarshal: %w", err)
	}

	if raw.Key == "" {
		return domain.Recipe{}, fmt.Errorf("missing required field: key")
	}
	if !recipeKeyPattern.MatchString(raw.Key) {
		return domain.Recipe{}, fmt.Errorf("invalid key %q (must match %s)", raw.Key, recipeKeyPattern)
	}
	if raw.Version == "" {
		return domain.Recipe{}, fmt.Errorf("recipe %q: missing version", raw.Key)
	}
	if raw.Name == "" {
		return domain.Recipe{}, fmt.Errorf("recipe %q: missing name", raw.Key)
	}

	out := domain.Recipe{
		Key:         raw.Key,
		Version:     raw.Version,
		Name:        raw.Name,
		Summary:     raw.Summary,
		Description: raw.Description,
	}

	overrideKeys := make(map[string]struct{}, len(raw.Overridable))
	for i, ov := range raw.Overridable {
		if ov.Key == "" {
			return domain.Recipe{}, fmt.Errorf("recipe %q: overridable[%d] missing key", raw.Key, i)
		}
		if _, dup := overrideKeys[ov.Key]; dup {
			return domain.Recipe{}, fmt.Errorf("recipe %q: duplicate overridable key %q", raw.Key, ov.Key)
		}
		overrideKeys[ov.Key] = struct{}{}
		switch ov.Type {
		case "string", "int":
		case "":
			return domain.Recipe{}, fmt.Errorf("recipe %q: overridable %q missing type", raw.Key, ov.Key)
		default:
			return domain.Recipe{}, fmt.Errorf("recipe %q: overridable %q has unsupported type %q", raw.Key, ov.Key, ov.Type)
		}
		out.Overridable = append(out.Overridable, domain.RecipeOverride{
			Key: ov.Key, Type: ov.Type, Default: ov.Default,
			Enum: ov.Enum, MaxLength: ov.MaxLength, Pattern: ov.Pattern,
		})
	}

	for _, p := range raw.Products {
		if p.Code == "" || p.Name == "" {
			return domain.Recipe{}, fmt.Errorf("recipe %q: product missing code or name", raw.Key)
		}
		out.Products = append(out.Products, domain.RecipeProduct(p))
	}

	meterKeys := make(map[string]struct{}, len(raw.Meters))
	for _, m := range raw.Meters {
		if m.Key == "" || m.Name == "" {
			return domain.Recipe{}, fmt.Errorf("recipe %q: meter missing key or name", raw.Key)
		}
		if _, dup := meterKeys[m.Key]; dup {
			return domain.Recipe{}, fmt.Errorf("recipe %q: duplicate meter key %q", raw.Key, m.Key)
		}
		meterKeys[m.Key] = struct{}{}
		out.Meters = append(out.Meters, domain.RecipeMeter(m))
	}

	ratingRuleKeys := make(map[string]struct{}, len(raw.RatingRules))
	for _, r := range raw.RatingRules {
		if r.Key == "" {
			return domain.Recipe{}, fmt.Errorf("recipe %q: rating_rule missing key", raw.Key)
		}
		if _, dup := ratingRuleKeys[r.Key]; dup {
			return domain.Recipe{}, fmt.Errorf("recipe %q: duplicate rating_rule key %q", raw.Key, r.Key)
		}
		ratingRuleKeys[r.Key] = struct{}{}
		mode := domain.PricingMode(r.Mode)
		switch mode {
		case domain.PricingFlat, domain.PricingGraduated, domain.PricingPackage:
		default:
			return domain.Recipe{}, fmt.Errorf("recipe %q: rating_rule %q invalid mode %q", raw.Key, r.Key, r.Mode)
		}
		dr := domain.RecipeRatingRule{
			Key: r.Key, Name: r.Name, Mode: mode, Currency: r.Currency,
			FlatAmountCents:        r.FlatAmountCents,
			PackageSize:            r.PackageSize,
			PackageAmountCents:     r.PackageAmountCents,
			OverageUnitAmountCents: r.OverageUnitAmountCents,
		}
		for _, t := range r.GraduatedTiers {
			dr.GraduatedTiers = append(dr.GraduatedTiers, domain.RatingTier(t))
		}
		out.RatingRules = append(out.RatingRules, dr)
	}

	for i, pr := range raw.PricingRules {
		if pr.Meter == "" || pr.RatingRule == "" {
			return domain.Recipe{}, fmt.Errorf("recipe %q: pricing_rules[%d] missing meter or rating_rule", raw.Key, i)
		}
		if _, ok := meterKeys[pr.Meter]; !ok {
			return domain.Recipe{}, fmt.Errorf("recipe %q: pricing_rules[%d] references unknown meter %q", raw.Key, i, pr.Meter)
		}
		if _, ok := ratingRuleKeys[pr.RatingRule]; !ok {
			return domain.Recipe{}, fmt.Errorf("recipe %q: pricing_rules[%d] references unknown rating_rule %q", raw.Key, i, pr.RatingRule)
		}
		mode := domain.AggregationMode(pr.AggregationMode)
		if pr.AggregationMode != "" && !mode.IsValid() {
			return domain.Recipe{}, fmt.Errorf("recipe %q: pricing_rules[%d] invalid aggregation_mode %q", raw.Key, i, pr.AggregationMode)
		}
		if pr.AggregationMode == "" {
			mode = domain.AggSum
		}
		out.PricingRules = append(out.PricingRules, domain.RecipePricingRule{
			MeterKey: pr.Meter, RatingRuleKey: pr.RatingRule,
			DimensionMatch: pr.DimensionMatch, AggregationMode: mode, Priority: pr.Priority,
		})
	}

	for _, p := range raw.Plans {
		if p.Code == "" || p.Name == "" {
			return domain.Recipe{}, fmt.Errorf("recipe %q: plan missing code or name", raw.Key)
		}
		interval := domain.BillingInterval(p.BillingInterval)
		switch interval {
		case domain.BillingMonthly, domain.BillingYearly:
		default:
			return domain.Recipe{}, fmt.Errorf("recipe %q: plan %q invalid billing_interval %q", raw.Key, p.Code, p.BillingInterval)
		}
		for _, mk := range p.Meters {
			if _, ok := meterKeys[mk]; !ok {
				return domain.Recipe{}, fmt.Errorf("recipe %q: plan %q references unknown meter %q", raw.Key, p.Code, mk)
			}
		}
		out.Plans = append(out.Plans, domain.RecipePlan{
			Code: p.Code, Name: p.Name, Currency: p.Currency, BillingInterval: interval,
			BaseAmountCents: p.BaseAmountCents, MeterKeys: p.Meters,
		})
	}

	if raw.Dunning != nil {
		dp := raw.Dunning.Policy
		out.DunningPolicy = &domain.RecipeDunningPolicy{
			Name: dp.Name, MaxRetries: dp.MaxRetries,
			IntervalsHours: dp.IntervalsHours, FinalAction: dp.FinalAction,
		}
	}

	if raw.Webhook != nil {
		out.Webhook = &domain.RecipeWebhook{
			Events: raw.Webhook.Events, URLPlaceholder: raw.Webhook.URLPlaceholder,
		}
	}

	if raw.SampleData != nil {
		planCode := raw.SampleData.Subscription.Plan
		out.SampleData = &domain.RecipeSampleData{
			Customer:     domain.RecipeSampleCustomer(raw.SampleData.Customer),
			Subscription: domain.RecipeSampleSubscription{PlanCode: planCode, TrialDays: raw.SampleData.Subscription.TrialDays},
		}
	}

	if err := validateTemplateReferences(out, overrideKeys); err != nil {
		return domain.Recipe{}, err
	}

	return out, nil
}
