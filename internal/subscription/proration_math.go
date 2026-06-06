package subscription

import (
	"math"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/platform/money"
)

// fullBillingCycleDays is the whole-day length of ONE full billing interval
// anchored at periodStart — the correct proration denominator.
//
// Proration divides the (new − old) period delta by the FULL cycle, never by
// the current period length. On a stub/partial period (mid-cycle signup) the
// current period is shorter than a cycle, so dividing by it over-charges
// upgrades / over-credits downgrades. Derived from the shared
// domain.AddBillingInterval so this matches the engine's day-1 stub base-fee
// proration (segDays/fullCycleDays) and BillOnPlanSwapImmediate exactly.
func fullBillingCycleDays(periodStart time.Time, interval domain.BillingInterval) int64 {
	end := domain.AddBillingInterval(periodStart, interval)
	return int64(math.Round(end.Sub(periodStart).Hours() / 24))
}

// prorationCents computes the immediate plan-change proration amount, in cents,
// for an in_advance subscription item changed mid-period.
//
// It is the difference between the new and old whole-period charge, scaled by
// the integer day ratio remainingDays/totalDays and banker's-rounded:
//
//	round_half_to_even( (newAmount - oldAmount) * remainingDays / totalDays )
//
// oldAmount / newAmount are the already-quantity-multiplied period charges in
// cents (basePerUnit * quantity). A positive result is an additional charge
// (upgrade); a negative result is a credit (downgrade / quantity reduction).
//
// The math is deliberately PURE INTEGER (ADR-042). The pre-ADR-042 path used
// float64 `diff * (remaining/total)` then math.RoundToEven, which introduced
// ULP drift on large amounts — a $36M delta could land a cent off because the
// day-ratio is not exactly representable in float64. Staying in int64 with
// money.RoundHalfToEven makes the result exact for every input that doesn't
// overflow int64 (the numerator (newAmount-oldAmount)*remainingDays overflows
// only past ~$3 quadrillion of delta, ~8 orders of magnitude beyond any real
// invoice). The operator-facing ProrationFactor is derived separately as a
// display-only float64 and never feeds the cents.
//
// Multiply-before-divide is intentional: it keeps the single rounding step at
// the end, so the result matches an exact rational computed at full precision.
//
// Returns 0 when totalDays <= 0 (no period span to prorate over) — callers
// treat a 0 result as "no proration artifact".
func prorationCents(oldAmount, newAmount, remainingDays, totalDays int64) int64 {
	if totalDays <= 0 {
		return 0
	}
	return money.RoundHalfToEven((newAmount-oldAmount)*remainingDays, totalDays)
}
