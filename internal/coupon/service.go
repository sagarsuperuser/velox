package coupon

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
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
		return domain.Coupon{}, fmt.Errorf("code is required")
	}
	if !codeRegexp.MatchString(code) {
		return domain.Coupon{}, fmt.Errorf("code must be 3-50 alphanumeric characters or dashes")
	}

	name := strings.TrimSpace(input.Name)
	if name == "" {
		return domain.Coupon{}, fmt.Errorf("name is required")
	}
	if len(name) > 200 {
		return domain.Coupon{}, fmt.Errorf("name must be at most 200 characters")
	}

	switch input.Type {
	case domain.CouponTypePercentage:
		if input.PercentOff <= 0 || input.PercentOff > 100 {
			return domain.Coupon{}, fmt.Errorf("percent_off must be between 0 and 100")
		}
	case domain.CouponTypeFixedAmount:
		if input.AmountOff <= 0 {
			return domain.Coupon{}, fmt.Errorf("amount_off must be greater than 0")
		}
		if input.AmountOff > 100_000_000 { // $1M cap
			return domain.Coupon{}, fmt.Errorf("amount_off cannot exceed 1,000,000.00")
		}
		cur := strings.TrimSpace(strings.ToUpper(input.Currency))
		if cur == "" {
			return domain.Coupon{}, fmt.Errorf("currency is required for fixed_amount coupons")
		}
		input.Currency = cur
	default:
		return domain.Coupon{}, fmt.Errorf("type must be 'percentage' or 'fixed_amount'")
	}

	if input.MaxRedemptions != nil && *input.MaxRedemptions < 1 {
		return domain.Coupon{}, fmt.Errorf("max_redemptions must be at least 1")
	}

	if input.ExpiresAt != nil && input.ExpiresAt.Before(time.Now()) {
		return domain.Coupon{}, fmt.Errorf("expires_at must be in the future")
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
		return domain.CouponRedemption{}, fmt.Errorf("code is required")
	}
	if input.CustomerID == "" {
		return domain.CouponRedemption{}, fmt.Errorf("customer_id is required")
	}
	if input.SubtotalCents <= 0 {
		return domain.CouponRedemption{}, fmt.Errorf("subtotal_cents must be greater than 0")
	}

	cpn, err := s.store.GetByCode(ctx, tenantID, code)
	if err != nil {
		return domain.CouponRedemption{}, fmt.Errorf("coupon not found")
	}

	if !cpn.Active {
		return domain.CouponRedemption{}, fmt.Errorf("coupon is not active")
	}

	if cpn.ExpiresAt != nil && cpn.ExpiresAt.Before(time.Now()) {
		return domain.CouponRedemption{}, fmt.Errorf("coupon has expired")
	}

	if cpn.MaxRedemptions != nil && cpn.TimesRedeemed >= *cpn.MaxRedemptions {
		return domain.CouponRedemption{}, fmt.Errorf("coupon has reached maximum redemptions")
	}

	if len(cpn.PlanIDs) > 0 && input.PlanID != "" {
		allowed := false
		for _, pid := range cpn.PlanIDs {
			if pid == input.PlanID {
				allowed = true
				break
			}
		}
		if !allowed {
			return domain.CouponRedemption{}, fmt.Errorf("coupon is not valid for this plan")
		}
	}

	discount := CalculateDiscount(cpn, input.SubtotalCents)
	if discount <= 0 {
		return domain.CouponRedemption{}, fmt.Errorf("discount amount is zero")
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
