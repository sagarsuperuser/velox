package coupon

// Stable, machine-readable error codes for coupon failures. These codes are
// part of the public API surface — integrators switch on them to deliver
// differential UX (e.g. "coupon_expired" → show a re-send-offer flow,
// "coupon_min_amount_not_met" → show the shortfall). Adding new codes is
// backwards-compatible; renaming or repurposing an existing code is a
// breaking change.
//
// Messages paired with these codes may evolve (clearer copy, i18n); the
// code string is the stable contract.
const (
	// Lookup / access. Fires on redeem/preview when the code can't be found
	// or is private-bound to a different customer (the two shapes are
	// merged on purpose so an attacker can't probe for valid codes).
	CodeNotFound = "coupon_not_found"

	// Lifecycle gates.
	CodeArchived = "coupon_archived"
	CodeExpired  = "coupon_expired"
	CodeMaxed    = "coupon_max_redemptions_reached"

	// Per-customer restrictions.
	CodePerCustomerMaxed = "coupon_per_customer_limit_reached"
	CodeFirstTimeOnly    = "coupon_first_time_only"

	// Scope mismatches between the coupon and the target invoice.
	CodePlanMismatch     = "coupon_plan_mismatch"
	CodeCurrencyMismatch = "coupon_currency_mismatch"
	CodeMinAmount        = "coupon_min_amount_not_met"

	// Creation / state.
	CodeCodeTaken       = "coupon_code_taken"
	CodeDiscountZero    = "coupon_discount_is_zero"
	CodeVersionConflict = "coupon_version_conflict"
)
