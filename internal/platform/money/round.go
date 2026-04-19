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
// Requires num >= 0 and denom > 0; billing amounts are always non-negative.
func RoundHalfToEven(num, denom int64) int64 {
	quotient := num / denom
	remainder := num % denom
	doubled := remainder * 2
	switch {
	case doubled < denom:
		return quotient
	case doubled > denom:
		return quotient + 1
	default:
		if quotient%2 == 0 {
			return quotient
		}
		return quotient + 1
	}
}
