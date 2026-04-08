package pricing

import (
	"context"

	"github.com/sagarsuperuser/velox/internal/domain"
)

type Store interface {
	// Rating rules
	CreateRatingRule(ctx context.Context, tenantID string, rule domain.RatingRuleVersion) (domain.RatingRuleVersion, error)
	GetRatingRule(ctx context.Context, tenantID, id string) (domain.RatingRuleVersion, error)
	ListRatingRules(ctx context.Context, filter RatingRuleFilter) ([]domain.RatingRuleVersion, error)

	// Meters
	CreateMeter(ctx context.Context, tenantID string, m domain.Meter) (domain.Meter, error)
	GetMeter(ctx context.Context, tenantID, id string) (domain.Meter, error)
	GetMeterByKey(ctx context.Context, tenantID, key string) (domain.Meter, error)
	ListMeters(ctx context.Context, tenantID string) ([]domain.Meter, error)
	UpdateMeter(ctx context.Context, tenantID string, m domain.Meter) (domain.Meter, error)

	// Plans
	CreatePlan(ctx context.Context, tenantID string, p domain.Plan) (domain.Plan, error)
	GetPlan(ctx context.Context, tenantID, id string) (domain.Plan, error)
	ListPlans(ctx context.Context, tenantID string) ([]domain.Plan, error)
	UpdatePlan(ctx context.Context, tenantID string, p domain.Plan) (domain.Plan, error)

	// Per-customer price overrides
	CreateOverride(ctx context.Context, tenantID string, o domain.CustomerPriceOverride) (domain.CustomerPriceOverride, error)
	GetOverride(ctx context.Context, tenantID, customerID, ruleID string) (domain.CustomerPriceOverride, error)
	ListOverrides(ctx context.Context, tenantID, customerID string) ([]domain.CustomerPriceOverride, error)
}

type RatingRuleFilter struct {
	TenantID       string
	RuleKey        string
	LifecycleState string
	LatestOnly     bool
}
