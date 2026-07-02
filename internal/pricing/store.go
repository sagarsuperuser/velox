package pricing

import (
	"context"
	"database/sql"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
)

type Store interface {
	// Rating rules. CreateRatingRule/Tx allocate the version number in
	// SQL (MAX(version)+1 per (tenant, rule_key)) — the caller's Version
	// field is ignored, so concurrent publishes can't race a Go-side
	// read-modify-write into a spurious 409.
	CreateRatingRule(ctx context.Context, tenantID string, rule domain.RatingRuleVersion) (domain.RatingRuleVersion, error)
	CreateRatingRuleTx(ctx context.Context, tx *sql.Tx, tenantID string, rule domain.RatingRuleVersion) (domain.RatingRuleVersion, error)
	GetRatingRule(ctx context.Context, tenantID, id string) (domain.RatingRuleVersion, error)
	// GetRuleByKeyAsOf resolves the version in force at asOf: the
	// highest active version created at or before asOf, or the earliest
	// active version when the key was born after asOf (a rule created
	// mid-period has no prior price to preserve — its first version is
	// the period's price; ADR-070). errs.ErrNotFound when the key has
	// no active versions at all.
	GetRuleByKeyAsOf(ctx context.Context, tenantID, ruleKey string, asOf time.Time) (domain.RatingRuleVersion, error)
	ListRatingRules(ctx context.Context, filter RatingRuleFilter) ([]domain.RatingRuleVersion, error)

	// Meters
	CreateMeter(ctx context.Context, tenantID string, m domain.Meter) (domain.Meter, error)
	CreateMeterTx(ctx context.Context, tx *sql.Tx, tenantID string, m domain.Meter) (domain.Meter, error)
	GetMeter(ctx context.Context, tenantID, id string) (domain.Meter, error)
	GetMeterByKey(ctx context.Context, tenantID, key string) (domain.Meter, error)
	ListMeters(ctx context.Context, tenantID string) ([]domain.Meter, error)
	UpdateMeter(ctx context.Context, tenantID string, m domain.Meter) (domain.Meter, error)

	// Plans
	CreatePlan(ctx context.Context, tenantID string, p domain.Plan) (domain.Plan, error)
	CreatePlanTx(ctx context.Context, tx *sql.Tx, tenantID string, p domain.Plan) (domain.Plan, error)
	GetPlan(ctx context.Context, tenantID, id string) (domain.Plan, error)
	ListPlans(ctx context.Context, tenantID string) ([]domain.Plan, error)
	UpdatePlan(ctx context.Context, tenantID string, p domain.Plan) (domain.Plan, error)

	// Per-customer price overrides — keyed by rule_key, resolved as-of
	// the billing period's open (ADR-070).
	CreateOverride(ctx context.Context, tenantID string, o domain.CustomerPriceOverride) (domain.CustomerPriceOverride, error)
	GetOverrideByKeyAsOf(ctx context.Context, tenantID, customerID, ruleKey string, asOf time.Time) (domain.CustomerPriceOverride, error)
	DeactivateOverride(ctx context.Context, tenantID, id string) error
	CountActiveOverridesByRuleKey(ctx context.Context, tenantID, ruleKey string) (int, error)
	ListOverrides(ctx context.Context, tenantID, customerID string) ([]domain.CustomerPriceOverride, error)

	// Meter pricing rules — N-rules-per-meter dispatch via dimension_match.
	// See docs/design-multi-dim-meters.md.
	UpsertMeterPricingRule(ctx context.Context, tenantID string, rule domain.MeterPricingRule) (domain.MeterPricingRule, error)
	UpsertMeterPricingRuleTx(ctx context.Context, tx *sql.Tx, tenantID string, rule domain.MeterPricingRule) (domain.MeterPricingRule, error)
	GetMeterPricingRule(ctx context.Context, tenantID, id string) (domain.MeterPricingRule, error)
	ListMeterPricingRulesByMeter(ctx context.Context, tenantID, meterID string) ([]domain.MeterPricingRule, error)
	DeleteMeterPricingRule(ctx context.Context, tenantID, id string) error
}

type RatingRuleFilter struct {
	TenantID       string
	RuleKey        string
	LifecycleState string
	LatestOnly     bool
}
