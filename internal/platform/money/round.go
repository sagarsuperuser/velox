// Package money holds small, dependency-free helpers for integer-cents math
// shared across billing, subscription proration, coupon discounts, and tax.
//
// Keeping these helpers in a platform package (not a domain package) avoids
// cross-domain imports: billing and subscription both depend on money, but
// neither depends on the other.
package money

// RoundHalfToEven computes num/denom rounded to the nearest integer using
// banker's rounding (round half to even). On exact ties (remainder = denom/2),
// rounds toward the nearest even integer instead of always rounding up.
//
// Why not half-up? Half-up introduces a systematic positive bias when rounding
// large batches of monetary amounts — over millions of invoices, this becomes
// a measurable accounting drift that auditors will flag. Half-to-even averages
// out to zero bias. IEEE 754 default, Python 3's round(), .NET decimal, and
// most financial/GAAP-adjacent systems use this rule.
//
// Requires denom > 0. num may be negative — downgrade/quantity-reduction
// proration passes a negative numerator. The rounding operates on the
// magnitude and reapplies the sign so negatives round symmetrically (half to
// even on the absolute value). Without this, Go's truncate-toward-zero integer
// division rounded every negative result toward zero, understating proration
// credits by up to 1 cent. Positive numerators are unaffected.
func RoundHalfToEven(num, denom int64) int64 {
	sign := int64(1)
	if num < 0 {
		sign = -1
		num = -num
	}
	quotient := num / denom
	remainder := num % denom
	doubled := remainder * 2
	switch {
	case doubled < denom:
		return sign * quotient
	case doubled > denom:
		return sign * (quotient + 1)
	default:
		if quotient%2 == 0 {
			return sign * quotient
		}
		return sign * (quotient + 1)
	}
}
