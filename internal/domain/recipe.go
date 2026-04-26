package domain

import "time"

// RecipeInstance is the DB-backed record of a recipe being instantiated for
// a tenant. The canonical entities a recipe creates (products, meters,
// pricing rules, plans, dunning policies, webhook endpoints) live in their
// own per-domain tables; RecipeInstance only tracks instantiation metadata
// so we can answer "is this recipe already installed?" idempotently and
// drive force-re-instantiate cleanup via CreatedObjects.
//
// See docs/design-recipes.md.
type RecipeInstance struct {
	ID             string         `json:"id"`
	TenantID       string         `json:"tenant_id,omitempty"`
	RecipeKey      string         `json:"recipe_key"`
	RecipeVersion  string         `json:"recipe_version"`
	Overrides      map[string]any `json:"overrides"`
	CreatedObjects CreatedObjects `json:"created_objects"`
	CreatedAt      time.Time      `json:"created_at"`
	CreatedBy      string         `json:"created_by,omitempty"`
}

// CreatedObjects is the per-role map of entity IDs a recipe instantiation
// produced. Persisted as JSONB on recipe_instances.created_object_ids and
// returned in the instantiate response so the dashboard can deep-link
// directly to the new plan / meter / webhook detail pages.
//
// Forced re-instantiation reads this map to delete the prior graph before
// running the recipe fresh. A recipe is free to leave any of these slices
// empty if its YAML doesn't declare that role.
type CreatedObjects struct {
	MeterIDs          []string `json:"meter_ids,omitempty"`
	RatingRuleIDs     []string `json:"rating_rule_ids,omitempty"`
	PricingRuleIDs    []string `json:"pricing_rule_ids,omitempty"`
	PlanIDs           []string `json:"plan_ids,omitempty"`
	DunningPolicyID   string   `json:"dunning_policy_id,omitempty"`
	WebhookEndpointID string   `json:"webhook_endpoint_id,omitempty"`
	CustomerIDs       []string `json:"customer_ids,omitempty"`
	SubscriptionIDs   []string `json:"subscription_ids,omitempty"`
}

// Recipe is the parsed, validated form of a recipe YAML file. Loaded once
// at process boot from the embedded FS and held in the registry; never
// mutated after load. Render() applies overrides and produces a
// RenderedRecipe ready for instantiation.
//
// JSON tags use snake_case to match the rest of /v1/* and the wire
// contract in docs/design-recipes.md. SampleData is omitted from JSON
// because it's an internal-only seed hint, not part of the public
// API surface.
type Recipe struct {
	Key         string           `json:"key"`
	Version     string           `json:"version"`
	Name        string           `json:"name"`
	Summary     string           `json:"summary"`
	Description string           `json:"description,omitempty"`
	Overridable []RecipeOverride `json:"overridable"`

	Meters        []RecipeMeter        `json:"meters"`
	RatingRules   []RecipeRatingRule   `json:"rating_rules"`
	PricingRules  []RecipePricingRule  `json:"pricing_rules"`
	Plans         []RecipePlan         `json:"plans"`
	DunningPolicy *RecipeDunningPolicy `json:"dunning_policy,omitempty"`
	Webhook       *RecipeWebhook       `json:"webhook,omitempty"`
	SampleData    *RecipeSampleData    `json:"-"`
}

// RecipeOverride is one override key declared in a recipe's `overridable`
// list. Drives validation at instantiate time and the override form in
// the dashboard's preview dialog.
type RecipeOverride struct {
	Key       string   `json:"key"`
	Type      string   `json:"type"` // "string" or "int"
	Default   any      `json:"default"`
	Enum      []string `json:"enum,omitempty"`
	MaxLength int      `json:"max_length,omitempty"`
	Pattern   string   `json:"pattern,omitempty"`
}

type RecipeMeter struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Unit        string `json:"unit"`
	Aggregation string `json:"aggregation"`
}

type RecipeRatingRule struct {
	Key                    string       `json:"key"`
	Name                   string       `json:"name,omitempty"`
	Mode                   PricingMode  `json:"mode"`
	Currency               string       `json:"currency"`
	FlatAmountCents        int64        `json:"flat_amount_cents,omitempty"`
	GraduatedTiers         []RatingTier `json:"graduated_tiers,omitempty"`
	PackageSize            int64        `json:"package_size,omitempty"`
	PackageAmountCents     int64        `json:"package_amount_cents,omitempty"`
	OverageUnitAmountCents int64        `json:"overage_unit_amount_cents,omitempty"`
}

type RecipePricingRule struct {
	MeterKey        string          `json:"meter_key"`
	RatingRuleKey   string          `json:"rating_rule_key"`
	DimensionMatch  map[string]any  `json:"dimension_match"`
	AggregationMode AggregationMode `json:"aggregation_mode"`
	Priority        int             `json:"priority"`
}

type RecipePlan struct {
	Code            string          `json:"code"`
	Name            string          `json:"name"`
	Currency        string          `json:"currency"`
	BillingInterval BillingInterval `json:"billing_interval"`
	BaseAmountCents int64           `json:"base_amount_cents"`
	MeterKeys       []string        `json:"meter_keys"`
}

type RecipeDunningPolicy struct {
	Name           string `json:"name"`
	MaxRetries     int    `json:"max_retries"`
	IntervalsHours []int  `json:"intervals_hours"`
	FinalAction    string `json:"final_action"`
}

type RecipeWebhook struct {
	Events         []string `json:"events"`
	URLPlaceholder string   `json:"url_placeholder"`
}

type RecipeSampleData struct {
	Customer     RecipeSampleCustomer     `json:"customer"`
	Subscription RecipeSampleSubscription `json:"subscription"`
}

type RecipeSampleCustomer struct {
	ExternalID  string `json:"external_id"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
}

type RecipeSampleSubscription struct {
	PlanCode  string `json:"plan_code"`
	TrialDays int    `json:"trial_days,omitempty"`
}
