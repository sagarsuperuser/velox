package coupon

import (
	"context"

	"github.com/sagarsuperuser/velox/internal/domain"
)

type Store interface {
	Create(ctx context.Context, tenantID string, coupon domain.Coupon) (domain.Coupon, error)
	Get(ctx context.Context, tenantID, id string) (domain.Coupon, error)
	GetByCode(ctx context.Context, tenantID, code string) (domain.Coupon, error)
	List(ctx context.Context, tenantID string) ([]domain.Coupon, error)
	Update(ctx context.Context, tenantID string, coupon domain.Coupon) (domain.Coupon, error)
	Deactivate(ctx context.Context, tenantID, id string) error
	IncrementRedemptions(ctx context.Context, tenantID, id string) error
	CreateRedemption(ctx context.Context, tenantID string, redemption domain.CouponRedemption) (domain.CouponRedemption, error)
	ListRedemptions(ctx context.Context, tenantID, couponID string) ([]domain.CouponRedemption, error)
}
