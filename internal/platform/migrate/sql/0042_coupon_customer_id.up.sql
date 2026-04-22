-- Private coupons: restrict a coupon to a single customer.
--
-- Enterprise sales flow: an AE negotiates a one-off discount for a specific
-- logo ("30% off for Acme for 6 months") and needs the coupon to be
-- (a) invisible/unusable to anyone else and
-- (b) tracked against that specific customer in reporting.
--
-- Public coupons leave customer_id NULL and behave exactly as before.
-- Private coupons set customer_id to the target and the redeem path
-- enforces the match — attempting to redeem customer A's private coupon
-- against customer B returns an invalid_request error rather than silently
-- granting the discount.
--
-- No FK to customers: if a customer is hard-deleted the coupon should not
-- cascade-delete or block deletion; an orphan private coupon is a no-op
-- (nobody can ever match) and the historical redemptions still make sense.

ALTER TABLE coupons
    ADD COLUMN customer_id TEXT;

-- Scoped index: only indexes the rows where customer_id is set, so public
-- coupons (the common case) don't pay the index-size cost. Supports the
-- "list private coupons for customer X" query the UI shows on the customer
-- detail page.
CREATE INDEX idx_coupons_tenant_customer ON coupons (tenant_id, customer_id)
    WHERE customer_id IS NOT NULL;
