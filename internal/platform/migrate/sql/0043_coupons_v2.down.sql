-- Reverse of 0043_coupons_v2.up.sql. Restores percent_off float, active
-- boolean, and drops the JSONB bags and redemption uniqueness guards.

DROP INDEX IF EXISTS idx_coupon_redemptions_idempotency;
DROP INDEX IF EXISTS idx_coupon_redemptions_invoice_unique;
DROP INDEX IF EXISTS idx_coupon_redemptions_subscription_unique;
ALTER TABLE coupon_redemptions DROP COLUMN idempotency_key;

ALTER TABLE coupons DROP COLUMN metadata;
ALTER TABLE coupons DROP COLUMN restrictions;

ALTER TABLE coupons ADD COLUMN active BOOLEAN NOT NULL DEFAULT true;
UPDATE coupons SET active = false WHERE archived_at IS NOT NULL;
DROP INDEX IF EXISTS idx_coupons_tenant_archived;
ALTER TABLE coupons DROP COLUMN archived_at;
CREATE INDEX idx_coupons_tenant_active ON coupons (tenant_id, active);

ALTER TABLE coupons ADD COLUMN percent_off NUMERIC(5,2) NOT NULL DEFAULT 0;
UPDATE coupons SET percent_off = (percent_off_bp::NUMERIC / 100)::NUMERIC(5,2)
WHERE percent_off_bp > 0;
