package domain

import (
	"errors"
	"math"
	"time"

	"github.com/shopspring/decimal"
)

type PricingMode string

const (
	PricingFlat      PricingMode = "flat"
	PricingGraduated PricingMode = "graduated"
	PricingPackage   PricingMode = "package"
)

type RatingTier struct {
	UpTo            int64 `json:"up_to"`
	UnitAmountCents int64 `json:"unit_amount_cents"`
}

type RatingRuleLifecycle string

const (
	RatingRuleDraft    RatingRuleLifecycle = "draft"
	RatingRuleActive   RatingRuleLifecycle = "active"
	RatingRuleArchived RatingRuleLifecycle = "archived"
)

type RatingRuleVersion struct {
	ID                     string              `json:"id"`
	TenantID               string              `json:"tenant_id,omitempty"`
	RuleKey                string              `json:"rule_key"`
	Name                   string              `json:"name"`
	Version                int                 `json:"version"`
	LifecycleState         RatingRuleLifecycle `json:"lifecycle_state,omitempty"`
	Mode                   PricingMode         `json:"mode"`
	Currency               string              `json:"currency"`
	FlatAmountCents        int64               `json:"flat_amount_cents"`
	GraduatedTiers         []RatingTier        `json:"graduated_tiers"`
	PackageSize            int64               `json:"package_size"`
	PackageAmountCents     int64               `json:"package_amount_cents"`
	OverageUnitAmountCents int64               `json:"overage_unit_amount_cents"`
	CreatedAt              time.Time           `json:"created_at"`
}

type Meter struct {
	ID                  string    `json:"id"`
	TenantID            string    `json:"tenant_id,omitempty"`
	Key                 string    `json:"key"`
	Name                string    `json:"name"`
	Unit                string    `json:"unit"`
	Aggregation         string    `json:"aggregation"`
	RatingRuleVersionID string    `json:"rating_rule_version_id"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

type BillingInterval string

const (
	BillingMonthly BillingInterval = "monthly"
	BillingYearly  BillingInterval = "yearly"
)

type PlanStatus string

const (
	PlanDraft    PlanStatus = "draft"
	PlanActive   PlanStatus = "active"
	PlanArchived PlanStatus = "archived"
)

type Plan struct {
	ID              string          `json:"id"`
	TenantID        string          `json:"tenant_id,omitempty"`
	Code            string          `json:"code"`
	Name            string          `json:"name"`
	Description     string          `json:"description,omitempty"`
	Currency        string          `json:"currency"`
	BillingInterval BillingInterval `json:"billing_interval"`
	Status          PlanStatus      `json:"status"`
	BaseAmountCents int64           `json:"base_amount_cents"`
	// TaxCode optionally overrides the tenant's default_product_tax_code for
	// this plan. Only consulted by the stripe_tax provider. Empty falls
	// back to tenant_settings.default_product_tax_code.
	TaxCode   string    `json:"tax_code,omitempty"`
	MeterIDs  []string  `json:"meter_ids"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

var ErrInvalidPricingConfig = errors.New("invalid pricing config")

// ErrAmountOverflow is returned when a pricing computation would exceed
// int64. Callers must treat this as a hard failure — a silent wrap would
// emit a negative invoice line in production.
var ErrAmountOverflow = errors.New("amount overflow: exceeds int64 range")

// maxInt64Decimal caches a decimal copy of math.MaxInt64 for the overflow
// guard at the int64-cents conversion boundary.
var maxInt64Decimal = decimal.NewFromInt(math.MaxInt64)

// ComputeAmountCents prices a decimal quantity through the given rating rule
// and returns the result rounded to whole cents. Quantity is decimal so that
// fractional usage primitives (GPU-hours, cached-token ratios) round-trip
// without precision loss. Tier boundaries (`up_to`, `package_size`) stay
// integer because pricing config is authored that way; the math walks tiers
// in decimal space and converts to int64 cents only at the final round.
//
// Rounding: half-to-even (banker's rounding) at the cent boundary. This is
// the same convention used elsewhere in the engine (money.RoundHalfToEven)
// and the IEEE 754 default — minimizes systematic bias on bulk invoices.
func ComputeAmountCents(rule RatingRuleVersion, quantity decimal.Decimal) (int64, error) {
	if quantity.IsNegative() {
		return 0, ErrInvalidPricingConfig
	}

	switch rule.Mode {
	case PricingFlat:
		if rule.FlatAmountCents < 0 {
			return 0, ErrInvalidPricingConfig
		}
		if quantity.IsZero() {
			return 0, nil
		}
		total := quantity.Mul(decimal.NewFromInt(rule.FlatAmountCents))
		return decimalToCents(total)

	case PricingGraduated:
		if len(rule.GraduatedTiers) == 0 {
			return 0, ErrInvalidPricingConfig
		}
		remaining := quantity
		lastUpper := int64(0)
		amount := decimal.Zero
		for i, tier := range rule.GraduatedTiers {
			if tier.UnitAmountCents < 0 || tier.UpTo < 0 {
				return 0, ErrInvalidPricingConfig
			}
			if remaining.IsZero() {
				break
			}
			if tier.UpTo == 0 {
				amount = amount.Add(remaining.Mul(decimal.NewFromInt(tier.UnitAmountCents)))
				remaining = decimal.Zero
				break
			}
			if tier.UpTo < lastUpper {
				return 0, ErrInvalidPricingConfig
			}
			tierCapacity := tier.UpTo - lastUpper
			if i == 0 {
				tierCapacity = tier.UpTo
			}
			if tierCapacity < 0 {
				return 0, ErrInvalidPricingConfig
			}
			capDec := decimal.NewFromInt(tierCapacity)
			consumed := remaining
			if consumed.GreaterThan(capDec) {
				consumed = capDec
			}
			amount = amount.Add(consumed.Mul(decimal.NewFromInt(tier.UnitAmountCents)))
			remaining = remaining.Sub(consumed)
			lastUpper = tier.UpTo
		}
		if remaining.IsPositive() {
			return 0, ErrInvalidPricingConfig
		}
		return decimalToCents(amount)

	case PricingPackage:
		if rule.PackageSize <= 0 || rule.PackageAmountCents < 0 || rule.OverageUnitAmountCents < 0 {
			return 0, ErrInvalidPricingConfig
		}
		if quantity.IsZero() {
			return 0, nil
		}
		pkgSize := decimal.NewFromInt(rule.PackageSize)
		fullPackages := quantity.Div(pkgSize).Floor()
		remainder := quantity.Sub(fullPackages.Mul(pkgSize))
		total := fullPackages.Mul(decimal.NewFromInt(rule.PackageAmountCents)).
			Add(remainder.Mul(decimal.NewFromInt(rule.OverageUnitAmountCents)))
		return decimalToCents(total)

	default:
		return 0, ErrInvalidPricingConfig
	}
}

// decimalToCents rounds a decimal cents value (potentially fractional after
// quantity multiplication) to int64 using banker's rounding. Returns
// ErrAmountOverflow if the rounded value would exceed int64 — silent wrap
// would emit a negative invoice line in production, so this is fail-loud.
func decimalToCents(d decimal.Decimal) (int64, error) {
	rounded := d.RoundBank(0)
	if rounded.GreaterThan(maxInt64Decimal) {
		return 0, ErrAmountOverflow
	}
	return rounded.IntPart(), nil
}
