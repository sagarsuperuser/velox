package importstripe

import (
	"errors"
	"fmt"
	"strings"

	"github.com/stripe/stripe-go/v82"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// MappedPrice is the result of translating a Stripe price into Velox shape.
// Phase 1 maps Stripe Price → Velox `domain.RatingRuleVersion` (mode=flat).
// The Plan field carries the resolved Velox plan (looked up via the price's
// product link before mapping is invoked) so the price importer can update
// the plan's pricing fields atomically with the rating-rule insert.
type MappedPrice struct {
	// RatingRule is the new RatingRuleVersion to insert. RuleKey is set to
	// the Stripe Price ID for round-trip lookup; Version is left at 0 here
	// — the service's CreateRatingRule path computes the next version
	// number based on existing rules with the same RuleKey.
	RatingRule domain.RatingRuleVersion
	// PlanCode is the Velox plan code (= Stripe product ID) the price
	// belongs to. The importer uses this to look up the plan and update
	// its currency / billing_interval / base_amount_cents to match the
	// price.
	PlanCode string
	// PlanUpdate captures the pricing fields the price importer should
	// patch onto the plan. Only currency, billing_interval, and
	// base_amount_cents are touched — name/description/status are owned
	// by the product import and never overwritten here.
	PlanUpdate PlanPricingUpdate
	// Notes accumulates non-fatal mapping observations surfaced in the
	// CSV report.
	Notes []string
}

// PlanPricingUpdate is the narrow subset of plan fields the price import
// can patch. Used by the importer to compose an UpdatePlan call without
// touching name/description.
type PlanPricingUpdate struct {
	Currency        string
	BillingInterval domain.BillingInterval
	BaseAmountCents int64
}

// Sentinel errors for unsupported Stripe price shapes. The price importer
// converts these into ActionError rows in the CSV report so operators can
// post-process them manually without aborting the run.
var (
	// ErrMapEmptyPriceID — Stripe price with no ID. Malformed fixture.
	ErrMapEmptyPriceID = errors.New("stripe price has empty id")

	// ErrPriceUnsupportedTiered — billing_scheme=tiered (graduated/volume).
	// Velox models graduated/volume tiers natively but mapping Stripe's
	// shape onto Velox's `RatingTier` requires careful translation of
	// `up_to=inf` and tier-mode semantics; deferred to a later slice.
	ErrPriceUnsupportedTiered = errors.New("stripe price uses tiered billing_scheme; not supported in Phase 1 (only flat per_unit prices)")

	// ErrPriceUnsupportedOneTime — type=one_time. Velox plans always model
	// recurring revenue; one-time prices belong to invoice line items, not
	// plans.
	ErrPriceUnsupportedOneTime = errors.New("stripe price has type=one_time; only recurring prices map to Velox plans")

	// ErrPriceUnsupportedMetered — recurring.usage_type=metered. These map
	// onto Velox meters, not plan base prices; deferred to a later slice
	// because the meter wiring requires a Stripe meter object lookup.
	ErrPriceUnsupportedMetered = errors.New("stripe price has usage_type=metered; only licensed (flat) recurring prices map to plan base prices in Phase 1")

	// ErrPriceMissingProduct — price has no product link. Stripe's API
	// always populates Product on list responses; this guards against
	// malformed fixtures.
	ErrPriceMissingProduct = errors.New("stripe price has no product reference")

	// ErrPriceMissingUnitAmount — billing_scheme=per_unit but UnitAmount
	// is not set. Stripe permits unit_amount_decimal as an alternative,
	// but Velox uses int64 cents — so prices that only carry the decimal
	// form are rejected with a clear note.
	ErrPriceMissingUnitAmount = errors.New("stripe price has no unit_amount; unit_amount_decimal alone is not supported")

	// ErrPriceUnsupportedInterval — Stripe supports day/week/month/year;
	// Velox only models monthly + yearly. day/week prices error out.
	ErrPriceUnsupportedInterval = errors.New("stripe price recurring.interval must be month or year (day/week not supported)")
)

// mapPrice translates a *stripe.Price into Velox shape. Pure: no DB / no
// Stripe API.
//
// Returns one of the sentinel errors above for any unsupported shape; the
// price importer converts those to ActionError rows so the run continues.
func mapPrice(price *stripe.Price) (MappedPrice, error) {
	if price == nil {
		return MappedPrice{}, errors.New("nil stripe price")
	}
	if strings.TrimSpace(price.ID) == "" {
		return MappedPrice{}, ErrMapEmptyPriceID
	}

	if price.Type == stripe.PriceTypeOneTime {
		return MappedPrice{}, ErrPriceUnsupportedOneTime
	}
	if price.BillingScheme == stripe.PriceBillingSchemeTiered {
		return MappedPrice{}, ErrPriceUnsupportedTiered
	}
	if price.Recurring != nil && price.Recurring.UsageType == stripe.PriceRecurringUsageTypeMetered {
		return MappedPrice{}, ErrPriceUnsupportedMetered
	}
	if price.Product == nil || strings.TrimSpace(price.Product.ID) == "" {
		return MappedPrice{}, ErrPriceMissingProduct
	}
	// Phase 1 only handles the simple flat per-unit case.
	if price.BillingScheme != "" && price.BillingScheme != stripe.PriceBillingSchemePerUnit {
		return MappedPrice{}, fmt.Errorf("stripe price has unrecognised billing_scheme %q", price.BillingScheme)
	}
	if price.UnitAmount == 0 && price.UnitAmountDecimal != 0 {
		// Stripe's docs note that unit_amount_decimal is set when the value
		// has more precision than int64 cents allows. Velox models cents
		// only, so we reject. Operators can adjust in Stripe (round to
		// whole cents) and re-run.
		return MappedPrice{}, ErrPriceMissingUnitAmount
	}

	// Recurring shape — we need an interval to set Velox plan.billing_interval.
	if price.Recurring == nil {
		return MappedPrice{}, errors.New("stripe price has type=recurring but recurring block is missing")
	}
	billingInterval, err := mapPriceInterval(price.Recurring.Interval, price.Recurring.IntervalCount)
	if err != nil {
		return MappedPrice{}, err
	}

	currency := strings.ToUpper(strings.TrimSpace(string(price.Currency)))
	if currency == "" {
		// Stripe always sets currency on real prices; defensive default.
		currency = "USD"
	}

	var notes []string
	if !price.Active {
		// Inactive Stripe prices still get imported as active rating
		// rules; lifecycle in Velox is independent. Operators can
		// archive the rule via the dashboard if they want to mirror
		// the Stripe state.
		notes = append(notes, "stripe price is inactive; imported as active rating rule (operators can archive in Velox)")
	}
	if price.TaxBehavior == stripe.PriceTaxBehaviorInclusive {
		notes = append(notes, "stripe price tax_behavior=inclusive; Velox tax-inclusive flag is set per-tenant, not per-rule — verify settings")
	}
	if price.TransformQuantity != nil {
		notes = append(notes, "stripe price transform_quantity ignored — Velox doesn't model bulk pricing transforms in Phase 1")
	}

	ruleName := strings.TrimSpace(price.Nickname)
	if ruleName == "" {
		ruleName = price.ID
	}

	rule := domain.RatingRuleVersion{
		// RuleKey = Stripe Price ID. Same rationale as Plan.Code = Product.ID:
		// idempotent round-trip without a dedicated external_id column.
		RuleKey:         price.ID,
		Name:            ruleName,
		LifecycleState:  domain.RatingRuleActive,
		Mode:            domain.PricingFlat,
		Currency:        currency,
		FlatAmountCents: price.UnitAmount,
	}

	return MappedPrice{
		RatingRule: rule,
		PlanCode:   price.Product.ID,
		PlanUpdate: PlanPricingUpdate{
			Currency:        currency,
			BillingInterval: billingInterval,
			BaseAmountCents: price.UnitAmount,
		},
		Notes: notes,
	}, nil
}

// mapPriceInterval translates Stripe's recurring.interval+interval_count
// onto Velox's BillingInterval enum. Stripe supports day/week/month/year
// with arbitrary positive interval_count; Velox only models monthly and
// yearly with implicit count=1. Anything else errors out so operators see
// the divergence in the report rather than getting a silently miscategorised
// plan.
func mapPriceInterval(interval stripe.PriceRecurringInterval, count int64) (domain.BillingInterval, error) {
	if count == 0 {
		count = 1
	}
	if count != 1 {
		return "", fmt.Errorf("stripe price recurring.interval_count=%d unsupported; Velox plans only model count=1", count)
	}
	switch interval {
	case stripe.PriceRecurringIntervalMonth:
		return domain.BillingMonthly, nil
	case stripe.PriceRecurringIntervalYear:
		return domain.BillingYearly, nil
	default:
		return "", ErrPriceUnsupportedInterval
	}
}
