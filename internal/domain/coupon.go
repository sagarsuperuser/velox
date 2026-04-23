package domain

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"
)

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

// CouponRestrictions is the long-tail of coupon eligibility gates kept
// out of the hot-path column list. New restrictions land here without a
// migration. Zero values mean "no restriction" so omitempty round-trips
// cleanly through the JSONB bag.
type CouponRestrictions struct {
	// MinAmountCents is the smallest subtotal (pre-discount) the coupon
	// applies to. Aimed at the "$10 off orders of $50+" shape.
	MinAmountCents int64 `json:"min_amount_cents,omitempty"`
	// FirstTimeCustomerOnly blocks redemption for a customer who already
	// has any prior invoice. Matches the standard acquisition-discount
	// pattern across Stripe/Chargebee/Recurly.
	FirstTimeCustomerOnly bool `json:"first_time_customer_only,omitempty"`
	// MaxRedemptionsPerCustomer caps per-customer usage. 0 means no cap
	// (global MaxRedemptions still applies). Guards against a single
	// customer draining a public promo intended for breadth.
	MaxRedemptionsPerCustomer int `json:"max_redemptions_per_customer,omitempty"`
}

// Scan reads a JSONB column into the struct. NULL / empty maps to the
// zero value so rows written before the column existed are still legal.
func (r *CouponRestrictions) Scan(src any) error {
	if src == nil {
		*r = CouponRestrictions{}
		return nil
	}
	b, ok := src.([]byte)
	if !ok {
		return fmt.Errorf("CouponRestrictions.Scan: expected []byte, got %T", src)
	}
	if len(b) == 0 {
		*r = CouponRestrictions{}
		return nil
	}
	return json.Unmarshal(b, r)
}

// Value serialises the struct for INSERT. Zero values round-trip as `{}`
// rather than nulls so the DB-level NOT NULL constraint is satisfied.
func (r CouponRestrictions) Value() (driver.Value, error) {
	return json.Marshal(r)
}

// IsZero reports whether every gate is at its zero value. Lets the
// service layer skip restrictions evaluation when nothing has been set,
// saving a redemptions-count query on the common public-coupon path.
func (r CouponRestrictions) IsZero() bool {
	return r.MinAmountCents == 0 &&
		!r.FirstTimeCustomerOnly &&
		r.MaxRedemptionsPerCustomer == 0
}

type Coupon struct {
	ID             string     `json:"id"`
	TenantID       string     `json:"tenant_id,omitempty"`
	Code           string     `json:"code"`
	Name           string     `json:"name"`
	Type           CouponType `json:"type"`
	AmountOff      int64      `json:"amount_off"`
	PercentOffBP   int        `json:"percent_off_bp"` // Basis points: 5050 = 50.50%
	Currency       string     `json:"currency"`
	MaxRedemptions *int       `json:"max_redemptions"`
	TimesRedeemed  int        `json:"times_redeemed"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	// PlanIDs gate the coupon to specific plans. Empty means any plan.
	PlanIDs         []string       `json:"plan_ids,omitempty"`
	Duration        CouponDuration `json:"duration"`
	DurationPeriods *int           `json:"duration_periods,omitempty"`
	Stackable       bool           `json:"stackable"`
	// CustomerID, when non-empty, scopes the coupon to a single customer
	// (the enterprise-negotiated private-discount flow).
	CustomerID string `json:"customer_id,omitempty"`
	// Restrictions is the extensible bag of long-tail gates — min order
	// amount, first-time-only, per-customer caps. See CouponRestrictions.
	Restrictions CouponRestrictions `json:"restrictions"`
	// Metadata is a tenant-controlled key/value bag, unopinionated. Stored
	// as raw JSONB bytes so the app code doesn't force a shape.
	Metadata []byte `json:"metadata,omitempty"`
	// ArchivedAt marks a user-initiated soft-delete. A non-nil value is
	// terminal for new redemptions; existing redemptions continue to
	// apply until they exhaust their own duration so ongoing contracts
	// aren't broken retroactively.
	ArchivedAt *time.Time `json:"archived_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	// Version is the optimistic-concurrency token. Every mutating write
	// (Update, Archive, Unarchive) bumps it by 1; the API echoes the
	// current value back via the ETag header, and clients replay it via
	// If-Match on subsequent writes to detect concurrent edits.
	Version int `json:"version"`
}

// Valid reports whether the coupon can accept new redemptions right now.
// Replaces the old "active" boolean: combines archived state, expiry,
// and the max-redemptions gate into the single question that actually
// matters at the point of redeem.
func (c Coupon) Valid() bool {
	if c.ArchivedAt != nil {
		return false
	}
	if c.ExpiresAt != nil && !c.ExpiresAt.After(time.Now()) {
		return false
	}
	if c.MaxRedemptions != nil && c.TimesRedeemed >= *c.MaxRedemptions {
		return false
	}
	return true
}

type CouponRedemption struct {
	ID             string `json:"id"`
	TenantID       string `json:"tenant_id,omitempty"`
	CouponID       string `json:"coupon_id"`
	CustomerID     string `json:"customer_id"`
	SubscriptionID string `json:"subscription_id,omitempty"`
	InvoiceID      string `json:"invoice_id,omitempty"`
	DiscountCents  int64  `json:"discount_cents"`
	PeriodsApplied int    `json:"periods_applied"`
	// IdempotencyKey ties the redemption to a client-supplied retry
	// token. Partial UNIQUE (tenant_id, idempotency_key) in the DB
	// guarantees that replaying the same key returns the same row
	// rather than creating a second redemption.
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	// VoidedAt is set when the underlying invoice is fully credited or
	// refunded. A voided redemption no longer counts toward the coupon's
	// usage — ApplyToInvoice skips it, and the coupon's times_redeemed is
	// decremented in the same tx that sets this field.
	VoidedAt  *time.Time `json:"voided_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
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

// CustomerDiscount is the operator-initiated attachment of a coupon to a
// customer. The billing engine consults the single active row per customer
// on every invoice generation and recomputes the discount against that
// invoice's actual subtotal, so a percentage coupon stays honest as
// subtotals vary month to month.
//
// Distinct from CouponRedemption — this is the standing attachment, not a
// per-invoice application record. Mirrors Stripe's customer.discount
// object. A revoked_at stamp terminates the assignment; the partial unique
// index on (tenant_id, customer_id) WHERE revoked_at IS NULL means the
// customer can then re-attach a different coupon without collision.
type CustomerDiscount struct {
	ID             string     `json:"id"`
	TenantID       string     `json:"tenant_id,omitempty"`
	CustomerID     string     `json:"customer_id"`
	CouponID       string     `json:"coupon_id"`
	PeriodsApplied int        `json:"periods_applied"`
	IdempotencyKey string     `json:"idempotency_key,omitempty"`
	Metadata       []byte     `json:"metadata,omitempty"`
	RevokedAt      *time.Time `json:"revoked_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}
