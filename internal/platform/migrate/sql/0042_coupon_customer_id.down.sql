DROP INDEX IF EXISTS idx_coupons_tenant_customer;

ALTER TABLE coupons
    DROP COLUMN IF EXISTS customer_id;
