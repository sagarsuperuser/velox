ALTER TABLE coupon_redemptions
    DROP COLUMN IF EXISTS periods_applied;

ALTER TABLE coupons
    DROP CONSTRAINT IF EXISTS coupons_duration_periods_check;

ALTER TABLE coupons
    DROP COLUMN IF EXISTS stackable,
    DROP COLUMN IF EXISTS duration_periods,
    DROP COLUMN IF EXISTS duration;
