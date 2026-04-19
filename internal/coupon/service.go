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
	Code           string            `json:"code"`
	Name           string            `json:"name"`
	Type           domain.CouponType `json:"type"`
	AmountOff      int64             `json:"amount_off"`
	PercentOff     float64           `json:"percent_off"`
	Currency       string            `json:"currency"`
	MaxRedemptions *int              `json:"max_redemptions"`
	ExpiresAt      *time.Time        `json:"expires_at,omitempty"`
	PlanIDs        []string          `json:"plan_ids,omitempty"`
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

	return s.store.Create(ctx, tenantID, domain.Coupon{
		Code:           code,
		Name:           name,
		Type:           input.Type,
		AmountOff:      input.AmountOff,
		PercentOff:     input.PercentOff,
		Currency:       input.Currency,
		MaxRedemptions: input.MaxRedemptions,
		ExpiresAt:      input.ExpiresAt,
		PlanIDs:        input.PlanIDs,
		Active:         true,
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

// ApplyToInvoice computes the total coupon discount (in cents) to apply against
// an invoice for the given subscription. It consults existing redemptions for
// the subscription, recomputes each coupon's discount against the supplied
// subtotal, and returns the best single match — Stripe's one-coupon-per-invoice
// model. Callers pass the *gross* subtotal (sum of line items, pre-discount
// pre-tax); the returned discount is clamped to the subtotal so the result can
// be safely subtracted without producing a negative running total.
//
// Side-effect-free: the function never writes to the store. The redemption
// record itself is what "attaches" a coupon to a subscription; applying it to
// an invoice does not consume the attachment.
//
// Inputs considered:
//   - redemptions with matching subscription_id (or customer_id when subscription_id is unset)
//   - coupon must still be Active and not past ExpiresAt at evaluation time
//   - if the coupon has PlanIDs set, planID must be in the list (otherwise ignored)
//
// Returns 0 with no error when no eligible redemption is found, so callers can
// call this unconditionally without a pre-check.
func (s *Service) ApplyToInvoice(ctx context.Context, tenantID, subscriptionID, planID string, subtotalCents int64) (int64, error) {
	if subscriptionID == "" || subtotalCents <= 0 {
		return 0, nil
	}

	redemptions, err := s.store.ListRedemptionsBySubscription(ctx, tenantID, subscriptionID)
	if err != nil {
		return 0, fmt.Errorf("list redemptions: %w", err)
	}
	if len(redemptions) == 0 {
		return 0, nil
	}

	now := time.Now()
	var best int64
	for _, r := range redemptions {
		cpn, err := s.store.Get(ctx, tenantID, r.CouponID)
		if err != nil {
			// Skip redemptions whose coupon can no longer be loaded — a stale
			// redemption row must not block billing. We log nothing here
			// because billing tick runs on a schedule and the caller can
			// surface a warning if it matters.
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

		d := CalculateDiscount(cpn, subtotalCents)
		if d > best {
			best = d
		}
	}
	return best, nil
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
