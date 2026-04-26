package recipe

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"strconv"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

// PricingWriter is the narrow tx-aware surface recipe.Service needs from
// pricing.Service. Defined here so the recipe package owns no cross-domain
// state and can be exercised with fakes in unit tests.
type PricingWriter interface {
	CreateRatingRuleTx(ctx context.Context, tx *sql.Tx, tenantID string, rule domain.RatingRuleVersion) (domain.RatingRuleVersion, error)
	CreateMeterTx(ctx context.Context, tx *sql.Tx, tenantID string, m domain.Meter) (domain.Meter, error)
	CreatePlanTx(ctx context.Context, tx *sql.Tx, tenantID string, p domain.Plan) (domain.Plan, error)
	UpsertMeterPricingRuleTx(ctx context.Context, tx *sql.Tx, tenantID string, rule domain.MeterPricingRule) (domain.MeterPricingRule, error)
}

// DunningWriter is the narrow tx-aware surface recipe.Service needs from
// dunning.Service.
type DunningWriter interface {
	UpsertPolicyTx(ctx context.Context, tx *sql.Tx, tenantID string, policy domain.DunningPolicy) (domain.DunningPolicy, error)
}

// WebhookWriter is the narrow tx-aware surface recipe.Service needs from
// webhook.Service.
type WebhookWriter interface {
	CreateEndpointTx(ctx context.Context, tx *sql.Tx, tenantID string, ep domain.WebhookEndpoint) (domain.WebhookEndpoint, error)
}

// Service is the orchestrator for the recipes feature: it answers
// "list recipes", "preview", "instantiate", and "uninstall". The
// canonical entities a recipe creates (meters, rating rules, plans,
// dunning policy, webhook endpoint) live in their own per-domain stores;
// this service threads a single transaction across the cross-domain
// writes so a recipe is either fully installed or not installed at all.
type Service struct {
	db       *postgres.DB
	store    Store
	registry *Registry
	pricing  PricingWriter
	dunning  DunningWriter
	webhook  WebhookWriter
}

// NewService wires the recipe service. registry must already be loaded
// via Load(); the constructor does no I/O.
func NewService(
	db *postgres.DB,
	store Store,
	registry *Registry,
	pricing PricingWriter,
	dunning DunningWriter,
	webhook WebhookWriter,
) *Service {
	return &Service{
		db:       db,
		store:    store,
		registry: registry,
		pricing:  pricing,
		dunning:  dunning,
		webhook:  webhook,
	}
}

// RecipeListItem is one entry in the GET /v1/recipes response — the
// canonical recipe metadata plus per-tenant installation state and a
// creates summary so the picker UI can render "1 meter · 9 pricing
// rules · monthly billing" without an extra preview round-trip. The
// dashboard uses Instantiated to flip the CTA from "Install" to
// "Manage / Uninstall".
type RecipeListItem struct {
	domain.Recipe
	Creates      RecipeCreates          `json:"creates"`
	Instantiated *domain.RecipeInstance `json:"instantiated,omitempty"`
}

// RecipeCreates is the per-role count of objects a recipe will produce
// when instantiated. Surfaced on list + detail responses so the
// dashboard can render summary chips without round-tripping preview.
// Counts mirror the shape of domain.CreatedObjects (which is per-ID),
// scaled down to integers for display.
type RecipeCreates struct {
	Meters           int `json:"meters"`
	RatingRules      int `json:"rating_rules"`
	PricingRules     int `json:"pricing_rules"`
	Plans            int `json:"plans"`
	DunningPolicies  int `json:"dunning_policies"`
	WebhookEndpoints int `json:"webhook_endpoints"`
}

// countCreates derives a RecipeCreates summary from a parsed recipe.
// Optional sections (DunningPolicy, Webhook) count as 1 when present, 0
// when absent — they're singletons in the YAML schema, not slices.
func countCreates(r domain.Recipe) RecipeCreates {
	c := RecipeCreates{
		Meters:       len(r.Meters),
		RatingRules:  len(r.RatingRules),
		PricingRules: len(r.PricingRules),
		Plans:        len(r.Plans),
	}
	if r.DunningPolicy != nil {
		c.DunningPolicies = 1
	}
	if r.Webhook != nil {
		c.WebhookEndpoints = 1
	}
	return c
}

// ListRecipes returns every recipe in the registry tagged with this
// tenant's installation state. One indexed read per recipe — fine at the
// v1 catalog size (5 recipes); revisit if the catalog grows past ~50.
func (s *Service) ListRecipes(ctx context.Context, tenantID string) ([]RecipeListItem, error) {
	recipes := s.registry.List()
	out := make([]RecipeListItem, 0, len(recipes))
	for _, r := range recipes {
		item := RecipeListItem{Recipe: r, Creates: countCreates(r)}
		inst, err := s.store.GetByKey(ctx, tenantID, r.Key)
		switch {
		case err == nil:
			instCopy := inst
			item.Instantiated = &instCopy
		case errors.Is(err, errs.ErrNotFound):
			// not installed for this tenant — leave Instantiated nil
		default:
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}

// RecipeDetail is the GET /v1/recipes/{key} response. Same fields as a
// list item plus the long-form description and full overridable_schema
// (already on the embedded domain.Recipe). Wrapping rather than
// returning bare domain.Recipe lets us co-emit the creates summary so
// the dashboard's picker drawer renders chips without a preview call.
type RecipeDetail struct {
	domain.Recipe
	Creates RecipeCreates `json:"creates"`
}

// GetRecipe returns the rendered-with-defaults form of a recipe so the
// dashboard can populate the override form. No DB I/O; pure registry.
func (s *Service) GetRecipe(key string) (RecipeDetail, error) {
	r, ok := s.registry.Get(key)
	if !ok {
		return RecipeDetail{}, errs.ErrNotFound
	}
	return RecipeDetail{Recipe: r, Creates: countCreates(r)}, nil
}

// ListInstances returns the recipe_instances rows for the tenant.
func (s *Service) ListInstances(ctx context.Context, tenantID string) ([]domain.RecipeInstance, error) {
	return s.store.List(ctx, tenantID)
}

// PreviewResult is the wire shape of POST /v1/recipes/{key}/preview.
// `objects` groups the would-be-created entities by role so the
// dashboard's preview panel renders one collapsible card per type.
// `warnings` surfaces non-fatal conditions (currency-vs-Stripe-account
// mismatches, placeholder webhook URLs, etc.); empty array in v1 — slot
// is in place so the contract stays stable when richer warnings land.
type PreviewResult struct {
	Key      string         `json:"key"`
	Version  string         `json:"version"`
	Objects  PreviewObjects `json:"objects"`
	Warnings []string       `json:"warnings"`
}

// PreviewObjects mirrors the recipe's object-graph sections. Optional
// pieces (DunningPolicy, Webhook) are emitted as 0-or-1-length slices so
// the wire shape is uniform — picker UI iterates without null guards.
// Empty arrays are emitted (no `omitempty`) for the same reason.
type PreviewObjects struct {
	Meters           []domain.RecipeMeter         `json:"meters"`
	RatingRules      []domain.RecipeRatingRule    `json:"rating_rules"`
	PricingRules     []domain.RecipePricingRule   `json:"pricing_rules"`
	Plans            []domain.RecipePlan          `json:"plans"`
	DunningPolicies  []domain.RecipeDunningPolicy `json:"dunning_policies"`
	WebhookEndpoints []domain.RecipeWebhook       `json:"webhook_endpoints"`
}

// Preview renders a recipe with caller-supplied overrides and returns
// the would-be-created object graph. Pure in-memory; no DB writes, no
// transactions. Cheap enough to call on every override-form keystroke.
func (s *Service) Preview(_ context.Context, recipeKey string, overrides map[string]any) (PreviewResult, error) {
	r, ok := s.registry.Get(recipeKey)
	if !ok {
		return PreviewResult{}, errs.ErrNotFound
	}
	rendered, err := renderRecipe(r, overrides)
	if err != nil {
		return PreviewResult{}, errs.Invalid("overrides", err.Error())
	}
	return previewResultFrom(rendered), nil
}

// previewResultFrom converts a rendered domain.Recipe into the public
// preview wire shape. Slices default to non-nil so the JSON encoder
// emits `[]` rather than `null` — picker UI maps without guards.
func previewResultFrom(r domain.Recipe) PreviewResult {
	out := PreviewResult{
		Key:     r.Key,
		Version: r.Version,
		Objects: PreviewObjects{
			Meters:           r.Meters,
			RatingRules:      r.RatingRules,
			PricingRules:     r.PricingRules,
			Plans:            r.Plans,
			DunningPolicies:  []domain.RecipeDunningPolicy{},
			WebhookEndpoints: []domain.RecipeWebhook{},
		},
		Warnings: []string{},
	}
	if out.Objects.Meters == nil {
		out.Objects.Meters = []domain.RecipeMeter{}
	}
	if out.Objects.RatingRules == nil {
		out.Objects.RatingRules = []domain.RecipeRatingRule{}
	}
	if out.Objects.PricingRules == nil {
		out.Objects.PricingRules = []domain.RecipePricingRule{}
	}
	if out.Objects.Plans == nil {
		out.Objects.Plans = []domain.RecipePlan{}
	}
	if r.DunningPolicy != nil {
		out.Objects.DunningPolicies = []domain.RecipeDunningPolicy{*r.DunningPolicy}
	}
	if r.Webhook != nil {
		out.Objects.WebhookEndpoints = []domain.RecipeWebhook{*r.Webhook}
	}
	return out
}

// InstantiateOptions controls one-off knobs for Instantiate. Force is
// reserved for v2 — passing Force=true currently returns InvalidState
// rather than silently dropping the flag, so the API contract stays
// honest about what's supported. CreatedBy is the actor (operator email
// or API key ID) recorded on recipe_instances.created_by for audit.
type InstantiateOptions struct {
	Force     bool
	CreatedBy string
}

// Instantiate builds a recipe's full object graph for tenantID under one
// transaction. Order: rating rules → meters → pricing rules → plan →
// (optional) dunning policy → (optional) webhook endpoint → instance row.
// Any failure rolls back the whole graph; partial state never reaches
// the tenant. Idempotent on (tenant_id, recipe_key): a second call on an
// already-installed recipe returns errs.ErrAlreadyExists with the
// existing instance ID surfaced via the WithCode error.
func (s *Service) Instantiate(
	ctx context.Context,
	tenantID, recipeKey string,
	overrides map[string]any,
	opts InstantiateOptions,
) (domain.RecipeInstance, error) {
	if opts.Force {
		return domain.RecipeInstance{}, errs.InvalidState(
			"force re-instantiation is not supported in v1; uninstall the existing instance first",
		)
	}

	r, ok := s.registry.Get(recipeKey)
	if !ok {
		return domain.RecipeInstance{}, errs.ErrNotFound
	}

	rendered, err := renderRecipe(r, overrides)
	if err != nil {
		return domain.RecipeInstance{}, errs.Invalid("overrides", err.Error())
	}

	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.RecipeInstance{}, err
	}
	defer postgres.Rollback(tx)

	// Idempotency check — if the recipe is already installed for this
	// tenant, fail fast rather than racing a second graph build.
	if existing, err := s.store.GetByKeyTx(ctx, tx, tenantID, recipeKey); err == nil {
		return domain.RecipeInstance{}, errs.AlreadyExists(
			"recipe_key",
			fmt.Sprintf("recipe %q is already instantiated as %s", recipeKey, existing.ID),
		)
	} else if !errors.Is(err, errs.ErrNotFound) {
		return domain.RecipeInstance{}, err
	}

	objs := domain.CreatedObjects{}

	// Rating rules first — meters and pricing rules reference them by ID.
	ratingRuleIDByKey := make(map[string]string, len(rendered.RatingRules))
	for _, rr := range rendered.RatingRules {
		created, err := s.pricing.CreateRatingRuleTx(ctx, tx, tenantID, ratingRuleFromRecipe(rr))
		if err != nil {
			return domain.RecipeInstance{}, fmt.Errorf("rating_rule %q: %w", rr.Key, err)
		}
		ratingRuleIDByKey[rr.Key] = created.ID
		objs.RatingRuleIDs = append(objs.RatingRuleIDs, created.ID)
	}

	// Meters — multi-dim meters use pricing rules for rate selection, so
	// rating_rule_version_id stays empty. Pricing rules below carry the
	// per-dimension rate bindings.
	meterIDByKey := make(map[string]string, len(rendered.Meters))
	for _, m := range rendered.Meters {
		created, err := s.pricing.CreateMeterTx(ctx, tx, tenantID, domain.Meter{
			Key:         m.Key,
			Name:        m.Name,
			Unit:        m.Unit,
			Aggregation: m.Aggregation,
		})
		if err != nil {
			return domain.RecipeInstance{}, fmt.Errorf("meter %q: %w", m.Key, err)
		}
		meterIDByKey[m.Key] = created.ID
		objs.MeterIDs = append(objs.MeterIDs, created.ID)
	}

	// Pricing rules — bind each (meter, dimension_match) tuple to a rating
	// rule. The recipe parser already verified meter_key and rating_rule_key
	// resolve, so the lookups here are safe.
	for i, pr := range rendered.PricingRules {
		meterID := meterIDByKey[pr.MeterKey]
		ruleID := ratingRuleIDByKey[pr.RatingRuleKey]
		created, err := s.pricing.UpsertMeterPricingRuleTx(ctx, tx, tenantID, domain.MeterPricingRule{
			MeterID:             meterID,
			RatingRuleVersionID: ruleID,
			DimensionMatch:      pr.DimensionMatch,
			AggregationMode:     pr.AggregationMode,
			Priority:            pr.Priority,
		})
		if err != nil {
			return domain.RecipeInstance{}, fmt.Errorf("pricing_rules[%d]: %w", i, err)
		}
		objs.PricingRuleIDs = append(objs.PricingRuleIDs, created.ID)
	}

	// Plan — references the meters created above by ID, not key.
	for _, p := range rendered.Plans {
		meterIDs := make([]string, 0, len(p.MeterKeys))
		for _, mk := range p.MeterKeys {
			meterIDs = append(meterIDs, meterIDByKey[mk])
		}
		created, err := s.pricing.CreatePlanTx(ctx, tx, tenantID, domain.Plan{
			Code:            p.Code,
			Name:            p.Name,
			Currency:        p.Currency,
			BillingInterval: p.BillingInterval,
			Status:          domain.PlanActive,
			BaseAmountCents: p.BaseAmountCents,
			MeterIDs:        meterIDs,
		})
		if err != nil {
			return domain.RecipeInstance{}, fmt.Errorf("plan %q: %w", p.Code, err)
		}
		objs.PlanIDs = append(objs.PlanIDs, created.ID)
	}

	// Optional dunning policy.
	if rendered.DunningPolicy != nil {
		dp := dunningFromRecipe(*rendered.DunningPolicy)
		created, err := s.dunning.UpsertPolicyTx(ctx, tx, tenantID, dp)
		if err != nil {
			return domain.RecipeInstance{}, fmt.Errorf("dunning policy: %w", err)
		}
		objs.DunningPolicyID = created.ID
	}

	// Optional webhook endpoint. Created inactive with the YAML's
	// url_placeholder; the operator activates it after pointing it at a
	// real URL via the dashboard.
	if rendered.Webhook != nil {
		secret, err := newWebhookSecret()
		if err != nil {
			return domain.RecipeInstance{}, err
		}
		created, err := s.webhook.CreateEndpointTx(ctx, tx, tenantID, domain.WebhookEndpoint{
			URL:    rendered.Webhook.URLPlaceholder,
			Secret: secret,
			Events: rendered.Webhook.Events,
			Active: false,
		})
		if err != nil {
			return domain.RecipeInstance{}, fmt.Errorf("webhook endpoint: %w", err)
		}
		objs.WebhookEndpointID = created.ID
	}

	inst, err := s.store.CreateTx(ctx, tx, domain.RecipeInstance{
		TenantID:       tenantID,
		RecipeKey:      recipeKey,
		RecipeVersion:  rendered.Version,
		Overrides:      copyOverrides(overrides),
		CreatedObjects: objs,
		CreatedBy:      opts.CreatedBy,
	})
	if err != nil {
		return domain.RecipeInstance{}, err
	}

	if err := tx.Commit(); err != nil {
		return domain.RecipeInstance{}, err
	}
	return inst, nil
}

// Uninstall removes the recipe_instance row only. The objects the recipe
// created (plans, meters, dunning policy, webhook endpoint) stay — the
// operator owns them once they exist, exactly like resources created
// directly via the API. Cascade-delete is intentionally deferred to v2;
// real plans may have live subscriptions and silent cascade would lose
// billing data.
func (s *Service) Uninstall(ctx context.Context, tenantID, instanceID string) error {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return err
	}
	defer postgres.Rollback(tx)
	if err := s.store.DeleteByIDTx(ctx, tx, tenantID, instanceID); err != nil {
		return err
	}
	return tx.Commit()
}

// ratingRuleFromRecipe maps the recipe's rating-rule shape to the
// canonical domain.RatingRuleVersion. New recipes always produce
// version=1, lifecycle=active rules — the recipe is the first-known
// version of that key for the tenant.
func ratingRuleFromRecipe(r domain.RecipeRatingRule) domain.RatingRuleVersion {
	name := r.Name
	if name == "" {
		name = r.Key
	}
	return domain.RatingRuleVersion{
		RuleKey:                r.Key,
		Name:                   name,
		Version:                1,
		LifecycleState:         domain.RatingRuleActive,
		Mode:                   r.Mode,
		Currency:               r.Currency,
		FlatAmountCents:        r.FlatAmountCents,
		GraduatedTiers:         r.GraduatedTiers,
		PackageSize:            r.PackageSize,
		PackageAmountCents:     r.PackageAmountCents,
		OverageUnitAmountCents: r.OverageUnitAmountCents,
	}
}

// dunningFromRecipe converts the recipe's dunning shape (max_retries +
// intervals_hours) to the engine's DunningPolicy (MaxRetryAttempts +
// retry_schedule of duration strings). intervals_hours is an int slice
// in the recipe so YAML stays human-friendly; the policy stores
// "Xh"-style strings to match how operators author policies in the API.
func dunningFromRecipe(p domain.RecipeDunningPolicy) domain.DunningPolicy {
	schedule := make([]string, 0, len(p.IntervalsHours))
	for _, h := range p.IntervalsHours {
		schedule = append(schedule, strconv.Itoa(h)+"h")
	}
	final := domain.DunningFinalAction(p.FinalAction)
	if final == "" {
		final = domain.DunningActionManualReview
	}
	return domain.DunningPolicy{
		Name:             p.Name,
		Enabled:          true,
		RetrySchedule:    schedule,
		MaxRetryAttempts: p.MaxRetries,
		FinalAction:      final,
		GracePeriodDays:  3,
	}
}

// newWebhookSecret mints a fresh whsec_-prefixed signing key. Same
// length and prefix as webhook.Service.CreateEndpoint so a recipe-created
// endpoint is indistinguishable from a hand-created one once the
// operator activates it.
func newWebhookSecret() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate webhook secret: %w", err)
	}
	return "whsec_" + hex.EncodeToString(buf), nil
}

// copyOverrides returns a defensive shallow copy of the caller-provided
// overrides map so the recipe_instances row can't be mutated through the
// caller's reference after persistence.
func copyOverrides(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	maps.Copy(out, in)
	return out
}
