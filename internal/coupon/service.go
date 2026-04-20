package coupon

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
)

var codeRegexp = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9\-]{1,48}[A-Za-z0-9]$`)

type Service struct {
	store Store
}

func NewService(store Store) *Service {
	return &Service{store: store}
}

type CreateInput struct {
	Code            string                `json:"code"`
	Name            string                `json:"name"`
	Type            domain.CouponType     `json:"type"`
	AmountOff       int64                 `json:"amount_off"`
	PercentOff      float64               `json:"percent_off"`
	Currency        string                `json:"currency"`
	MaxRedemptions  *int                  `json:"max_redemptions"`
	ExpiresAt       *time.Time            `json:"expires_at,omitempty"`
	PlanIDs         []string              `json:"plan_ids,omitempty"`
	Duration        domain.CouponDuration `json:"duration,omitempty"`
	DurationPeriods *int                  `json:"duration_periods,omitempty"`
	Stackable       bool                  `json:"stackable"`
}

func (s *Service) Create(ctx context.Context, tenantID string, input CreateInput) (domain.Coupon, error) {
	code := strings.TrimSpace(strings.ToUpper(input.Code))
	if code == "" {
		return domain.Coupon{}, errs.Required("code")
	}
	if !codeRegexp.MatchString(code) {
		return domain.Coupon{}, errs.Invalid("code", "must be 3-50 alphanumeric characters or dashes")
	}

	name := strings.TrimSpace(input.Name)
	if name == "" {
		return domain.Coupon{}, errs.Required("name")
	}
	if len(name) > 200 {
		return domain.Coupon{}, errs.Invalid("name", "must be at most 200 characters")
	}

	switch input.Type {
	case domain.CouponTypePercentage:
		if input.PercentOff <= 0 || input.PercentOff > 100 {
			return domain.Coupon{}, errs.Invalid("percent_off", "must be between 0 and 100")
		}
	case domain.CouponTypeFixedAmount:
		if input.AmountOff <= 0 {
			return domain.Coupon{}, errs.Invalid("amount_off", "must be greater than 0")
		}
		if input.AmountOff > 100_000_000 { // $1M cap
			return domain.Coupon{}, errs.Invalid("amount_off", "cannot exceed 1,000,000.00")
		}
		cur := strings.TrimSpace(strings.ToUpper(input.Currency))
		if cur == "" {
			return domain.Coupon{}, errs.Required("currency")
		}
		input.Currency = cur
	default:
		return domain.Coupon{}, errs.Invalid("type", "must be 'percentage' or 'fixed_amount'")
	}

	if input.MaxRedemptions != nil && *input.MaxRedemptions < 1 {
		return domain.Coupon{}, errs.Invalid("max_redemptions", "must be at least 1")
	}

	if input.ExpiresAt != nil && input.ExpiresAt.Before(time.Now()) {
		return domain.Coupon{}, errs.Invalid("expires_at", "must be in the future")
	}

	// Duration defaults to Forever so older API clients that don't send the
	// field land on the same behaviour they had before FEAT-6.
	duration := input.Duration
	if duration == "" {
		duration = domain.CouponDurationForever
	}
	switch duration {
	case domain.CouponDurationOnce, domain.CouponDurationForever:
		if input.DurationPeriods != nil {
			return domain.Coupon{}, errs.Invalid("duration_periods",
				"only valid when duration is 'repeating'")
		}
	case domain.CouponDurationRepeating:
		if input.DurationPeriods == nil || *input.DurationPeriods < 1 {
			return domain.Coupon{}, errs.Invalid("duration_periods",
				"required and must be at least 1 when duration is 'repeating'")
		}
	default:
		return domain.Coupon{}, errs.Invalid("duration",
			"must be 'once', 'repeating', or 'forever'")
	}

	return s.store.Create(ctx, tenantID, domain.Coupon{
		Code:            code,
		Name:            name,
		Type:            input.Type,
		AmountOff:       input.AmountOff,
		PercentOff:      input.PercentOff,
		Currency:        input.Currency,
		MaxRedemptions:  input.MaxRedemptions,
		ExpiresAt:       input.ExpiresAt,
		PlanIDs:         input.PlanIDs,
		Duration:        duration,
		DurationPeriods: input.DurationPeriods,
		Stackable:       input.Stackable,
		Active:          true,
	})
}

func (s *Service) Get(ctx context.Context, tenantID, id string) (domain.Coupon, error) {
	return s.store.Get(ctx, tenantID, id)
}

func (s *Service) List(ctx context.Context, tenantID string) ([]domain.Coupon, error) {
	return s.store.List(ctx, tenantID)
}

func (s *Service) Deactivate(ctx context.Context, tenantID, id string) error {
	return s.store.Deactivate(ctx, tenantID, id)
}

type RedeemInput struct {
	Code           string `json:"code"`
	CustomerID     string `json:"customer_id"`
	SubscriptionID string `json:"subscription_id,omitempty"`
	InvoiceID      string `json:"invoice_id,omitempty"`
	PlanID         string `json:"plan_id,omitempty"`
	SubtotalCents  int64  `json:"subtotal_cents"`
}

func (s *Service) Redeem(ctx context.Context, tenantID string, input RedeemInput) (domain.CouponRedemption, error) {
	code := strings.TrimSpace(strings.ToUpper(input.Code))
	if code == "" {
		return domain.CouponRedemption{}, errs.Required("code")
	}
	if input.CustomerID == "" {
		return domain.CouponRedemption{}, errs.Required("customer_id")
	}
	if input.SubtotalCents <= 0 {
		return domain.CouponRedemption{}, errs.Invalid("subtotal_cents", "must be greater than 0")
	}

	cpn, err := s.store.GetByCode(ctx, tenantID, code)
	if err != nil {
		return domain.CouponRedemption{}, errs.Invalid("code", "coupon not found")
	}

	if !cpn.Active {
		return domain.CouponRedemption{}, errs.InvalidState("coupon is not active")
	}

	if cpn.ExpiresAt != nil && cpn.ExpiresAt.Before(time.Now()) {
		return domain.CouponRedemption{}, errs.InvalidState("coupon has expired")
	}

	if cpn.MaxRedemptions != nil && cpn.TimesRedeemed >= *cpn.MaxRedemptions {
		return domain.CouponRedemption{}, errs.InvalidState("coupon has reached maximum redemptions")
	}

	if len(cpn.PlanIDs) > 0 && input.PlanID != "" && !slices.Contains(cpn.PlanIDs, input.PlanID) {
		return domain.CouponRedemption{}, errs.Invalid("plan_id", "coupon is not valid for this plan")
	}

	discount := CalculateDiscount(cpn, input.SubtotalCents)
	if discount <= 0 {
		return domain.CouponRedemption{}, errs.InvalidState("discount amount is zero")
	}

	// Increment redemption count
	if err := s.store.IncrementRedemptions(ctx, tenantID, cpn.ID); err != nil {
		return domain.CouponRedemption{}, fmt.Errorf("increment redemptions: %w", err)
	}

	return s.store.CreateRedemption(ctx, tenantID, domain.CouponRedemption{
		CouponID:       cpn.ID,
		CustomerID:     input.CustomerID,
		SubscriptionID: input.SubscriptionID,
		InvoiceID:      input.InvoiceID,
		DiscountCents:  discount,
	})
}

func (s *Service) ListRedemptions(ctx context.Context, tenantID, couponID string) ([]domain.CouponRedemption, error) {
	return s.store.ListRedemptions(ctx, tenantID, couponID)
}

// ApplyToInvoice computes the coupon discount for an invoice on the given
// subscription. It walks active redemptions, filters by eligibility
// (coupon still active, not expired, plan match, and duration not yet
// exhausted), then either picks the best single coupon or combines
// stackable coupons — whichever policy is correct for the mix.
//
// Stacking rules:
//   - If any eligible coupon is non-stackable, only the single largest
//     discount wins (pre-FEAT-6 "best one" behaviour, preserved so
//     operators who haven't opted into stacking see no behaviour change).
//   - If every eligible coupon is stackable, percent_offs sum (capped at
//     100%) and fixed amount_offs sum, each applied to the gross subtotal;
//     the combined discount is clamped to the subtotal.
//
// Side-effect-free: no store writes. The caller (billing engine) owns the
// "mark applied" step so a failed invoice create doesn't burn a period of
// a repeating coupon.
func (s *Service) ApplyToInvoice(ctx context.Context, tenantID, subscriptionID, planID string, subtotalCents int64) (domain.CouponDiscountResult, error) {
	if subscriptionID == "" || subtotalCents <= 0 {
		return domain.CouponDiscountResult{}, nil
	}

	redemptions, err := s.store.ListRedemptionsBySubscription(ctx, tenantID, subscriptionID)
	if err != nil {
		return domain.CouponDiscountResult{}, fmt.Errorf("list redemptions: %w", err)
	}
	if len(redemptions) == 0 {
		return domain.CouponDiscountResult{}, nil
	}

	now := time.Now()

	// eligible bundles each surviving coupon with its redemption so the
	// stacking step can refer back to both without a second store lookup.
	type eligible struct {
		coupon     domain.Coupon
		redemption domain.CouponRedemption
	}
	var pool []eligible

	for _, r := range redemptions {
		cpn, err := s.store.Get(ctx, tenantID, r.CouponID)
		if err != nil {
			// Stale redemption row whose coupon has been deleted or is
			// behind an RLS boundary — skip silently so one bad row can't
			// block billing. Logging happens at the billing engine layer.
			continue
		}
		if !cpn.Active {
			continue
		}
		if cpn.ExpiresAt != nil && cpn.ExpiresAt.Before(now) {
			continue
		}
		if len(cpn.PlanIDs) > 0 && planID != "" && !slices.Contains(cpn.PlanIDs, planID) {
			continue
		}
		if !durationHasPeriodLeft(cpn, r) {
			continue
		}
		pool = append(pool, eligible{coupon: cpn, redemption: r})
	}

	if len(pool) == 0 {
		return domain.CouponDiscountResult{}, nil
	}

	// If any eligible coupon is non-stackable, fall back to "best single
	// wins". Mixing a non-stackable with stackables is ambiguous and
	// trying to combine them would surprise operators who set
	// stackable=false specifically to prevent compounding.
	anyNonStackable := slices.ContainsFunc(pool, func(e eligible) bool { return !e.coupon.Stackable })
	if anyNonStackable {
		var bestIdx int
		var bestCents int64
		for i, e := range pool {
			d := CalculateDiscount(e.coupon, subtotalCents)
			if d > bestCents {
				bestCents = d
				bestIdx = i
			}
		}
		if bestCents == 0 {
			return domain.CouponDiscountResult{}, nil
		}
		return domain.CouponDiscountResult{
			Cents:         bestCents,
			RedemptionIDs: []string{pool[bestIdx].redemption.ID},
		}, nil
	}

	// All stackable — combine percent_offs (capped 100%) and amount_offs.
	// We intentionally apply percent and fixed against the gross subtotal
	// in parallel rather than sequentially; predictability beats the
	// marginal accuracy gain of compounding order, and matches operator
	// expectations ("I stacked 10% + $5 off $100 → I save $15").
	var percentSum float64
	var fixedSum int64
	for _, e := range pool {
		switch e.coupon.Type {
		case domain.CouponTypePercentage:
			percentSum += e.coupon.PercentOff
		case domain.CouponTypeFixedAmount:
			fixedSum += e.coupon.AmountOff
		}
	}
	percentSum = min(percentSum, 100)
	percentCents := int64(math.RoundToEven(float64(subtotalCents) * percentSum / 100))
	total := min(percentCents+fixedSum, subtotalCents)
	if total <= 0 {
		return domain.CouponDiscountResult{}, nil
	}

	ids := make([]string, 0, len(pool))
	for _, e := range pool {
		ids = append(ids, e.redemption.ID)
	}
	return domain.CouponDiscountResult{Cents: total, RedemptionIDs: ids}, nil
}

// durationHasPeriodLeft reports whether the redemption still has at least
// one billing period to apply against under the coupon's duration rule.
// Forever always returns true; once exhausts after the first application;
// repeating exhausts once periods_applied reaches duration_periods.
func durationHasPeriodLeft(c domain.Coupon, r domain.CouponRedemption) bool {
	switch c.Duration {
	case domain.CouponDurationOnce:
		return r.PeriodsApplied < 1
	case domain.CouponDurationRepeating:
		if c.DurationPeriods == nil {
			// Misconfigured: treat as exhausted rather than forever so a
			// bad row doesn't silently grant a never-ending discount.
			return false
		}
		return r.PeriodsApplied < *c.DurationPeriods
	case domain.CouponDurationForever, "":
		// Empty duration is legacy pre-migration data; treat as forever
		// to preserve the old behaviour where no duration column existed.
		return true
	default:
		return false
	}
}

// MarkPeriodsApplied advances the periods_applied counter on each
// redemption by one. Callers invoke this after the invoice that consumed
// the discount commits — doing it beforehand would burn a period of a
// repeating coupon even if the invoice create rolled back. Per-redemption
// failures are returned as a joined error but the loop continues so a
// single bad row doesn't starve the others.
func (s *Service) MarkPeriodsApplied(ctx context.Context, tenantID string, redemptionIDs []string) error {
	if len(redemptionIDs) == 0 {
		return nil
	}
	var errs_ []error
	for _, id := range redemptionIDs {
		if id == "" {
			continue
		}
		if err := s.store.IncrementPeriodsApplied(ctx, tenantID, id); err != nil {
			errs_ = append(errs_, fmt.Errorf("redemption %s: %w", id, err))
		}
	}
	if len(errs_) == 0 {
		return nil
	}
	// Return the first error — the others are logged-or-equivalent at
	// this layer since we've already committed the invoice and there's
	// nothing to undo.
	return errs_[0]
}

// CalculateDiscount computes the discount amount in cents for a given coupon and subtotal.
func CalculateDiscount(c domain.Coupon, subtotalCents int64) int64 {
	if subtotalCents <= 0 {
		return 0
	}

	switch c.Type {
	case domain.CouponTypePercentage:
		discount := int64(math.RoundToEven(float64(subtotalCents) * c.PercentOff / 100))
		if discount > subtotalCents {
			return subtotalCents
		}
		return discount
	case domain.CouponTypeFixedAmount:
		if c.AmountOff > subtotalCents {
			return subtotalCents
		}
		return c.AmountOff
	default:
		return 0
	}
}
