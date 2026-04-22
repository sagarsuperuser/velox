package domain

import "time"

type CouponType string

const (
	CouponTypePercentage  CouponType = "percentage"
	CouponTypeFixedAmount CouponType = "fixed_amount"
)

// CouponDuration mirrors Stripe's coupon.duration: once applies for a
// single invoice, repeating applies for DurationPeriods billing cycles,
// forever applies indefinitely. The default for new coupons is Forever so
// code that doesn't opt in matches the pre-FEAT-6 behaviour.
type CouponDuration string

const (
	CouponDurationOnce      CouponDuration = "once"
	CouponDurationRepeating CouponDuration = "repeating"
	CouponDurationForever   CouponDuration = "forever"
)

type Coupon struct {
	ID              string         `json:"id"`
	TenantID        string         `json:"tenant_id,omitempty"`
	Code            string         `json:"code"`
	Name            string         `json:"name"`
	Type            CouponType     `json:"type"`
	AmountOff       int64          `json:"amount_off"`
	PercentOff      float64        `json:"percent_off"`    // Deprecated: use PercentOffBP
	PercentOffBP    int            `json:"percent_off_bp"` // Basis points (5050 = 50.50%)
	Currency        string         `json:"currency"`
	MaxRedemptions  *int           `json:"max_redemptions"`
	TimesRedeemed   int            `json:"times_redeemed"`
	ExpiresAt       *time.Time     `json:"expires_at,omitempty"`
	PlanIDs         []string       `json:"plan_ids,omitempty"` // If set, coupon only applies to these plans
	Duration        CouponDuration `json:"duration"`
	DurationPeriods *int           `json:"duration_periods,omitempty"` // Required when Duration==repeating
	Stackable       bool           `json:"stackable"`
	Active          bool           `json:"active"`
	// CustomerID, when non-empty, scopes the coupon to a single customer.
	// Enterprise-negotiated private discounts use this so customer A's
	// one-off terms can't be redeemed by customer B. Empty means public.
	CustomerID string    `json:"customer_id,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

type CouponRedemption struct {
	ID             string    `json:"id"`
	TenantID       string    `json:"tenant_id,omitempty"`
	CouponID       string    `json:"coupon_id"`
	CustomerID     string    `json:"customer_id"`
	SubscriptionID string    `json:"subscription_id,omitempty"`
	InvoiceID      string    `json:"invoice_id,omitempty"`
	DiscountCents  int64     `json:"discount_cents"`
	PeriodsApplied int       `json:"periods_applied"`
	CreatedAt      time.Time `json:"created_at"`
}

// CouponDiscountResult is what coupon-apply returns to the billing side.
// Lives in domain because billing and subscription both consume it but
// can't import each other — domain is their shared vocabulary.
// RedemptionIDs lists the redemptions that actually contributed so the
// caller can advance periods_applied after the invoice commits.
type CouponDiscountResult struct {
	Cents         int64    `json:"cents"`
	RedemptionIDs []string `json:"redemption_ids,omitempty"`
}
