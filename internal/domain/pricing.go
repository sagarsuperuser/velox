package domain

import (
	"errors"
	"math"
	"time"
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

// mulNonNegative multiplies two non-negative int64 values and flags
// overflow. Callers must validate inputs >= 0 before calling.
func mulNonNegative(a, b int64) (int64, bool) {
	if a == 0 || b == 0 {
		return 0, false
	}
	if b > math.MaxInt64/a {
		return 0, true
	}
	return a * b, false
}

// addNonNegative adds two non-negative int64 values and flags overflow.
// Callers must validate inputs >= 0 before calling.
func addNonNegative(a, b int64) (int64, bool) {
	if a > math.MaxInt64-b {
		return 0, true
	}
	return a + b, false
}

func ComputeAmountCents(rule RatingRuleVersion, quantity int64) (int64, error) {
	if quantity < 0 {
		return 0, ErrInvalidPricingConfig
	}

	switch rule.Mode {
	case PricingFlat:
		if rule.FlatAmountCents < 0 {
			return 0, ErrInvalidPricingConfig
		}
		if quantity == 0 {
			return 0, nil
		}
		total, overflow := mulNonNegative(quantity, rule.FlatAmountCents)
		if overflow {
			return 0, ErrAmountOverflow
		}
		return total, nil

	case PricingGraduated:
		if len(rule.GraduatedTiers) == 0 {
			return 0, ErrInvalidPricingConfig
		}
		remaining := quantity
		lastUpper := int64(0)
		amount := int64(0)
		for i, tier := range rule.GraduatedTiers {
			if tier.UnitAmountCents < 0 || tier.UpTo < 0 {
				return 0, ErrInvalidPricingConfig
			}
			if remaining == 0 {
				break
			}
			if tier.UpTo == 0 {
				tierAmt, overflow := mulNonNegative(remaining, tier.UnitAmountCents)
				if overflow {
					return 0, ErrAmountOverflow
				}
				amount, overflow = addNonNegative(amount, tierAmt)
				if overflow {
					return 0, ErrAmountOverflow
				}
				remaining = 0
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
			consumed := min(remaining, tierCapacity)
			tierAmt, overflow := mulNonNegative(consumed, tier.UnitAmountCents)
			if overflow {
				return 0, ErrAmountOverflow
			}
			amount, overflow = addNonNegative(amount, tierAmt)
			if overflow {
				return 0, ErrAmountOverflow
			}
			remaining -= consumed
			lastUpper = tier.UpTo
		}
		if remaining > 0 {
			return 0, ErrInvalidPricingConfig
		}
		return amount, nil

	case PricingPackage:
		if rule.PackageSize <= 0 || rule.PackageAmountCents < 0 || rule.OverageUnitAmountCents < 0 {
			return 0, ErrInvalidPricingConfig
		}
		if quantity == 0 {
			return 0, nil
		}
		fullPackages := quantity / rule.PackageSize
		remainder := quantity % rule.PackageSize
		packagesAmt, overflow := mulNonNegative(fullPackages, rule.PackageAmountCents)
		if overflow {
			return 0, ErrAmountOverflow
		}
		overageAmt, overflow := mulNonNegative(remainder, rule.OverageUnitAmountCents)
		if overflow {
			return 0, ErrAmountOverflow
		}
		total, overflow := addNonNegative(packagesAmt, overageAmt)
		if overflow {
			return 0, ErrAmountOverflow
		}
		return total, nil

	default:
		return 0, ErrInvalidPricingConfig
	}
}
