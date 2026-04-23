DROP INDEX IF EXISTS idx_coupon_redemptions_subscription_active;

ALTER TABLE coupon_redemptions
    DROP COLUMN IF EXISTS voided_at;
