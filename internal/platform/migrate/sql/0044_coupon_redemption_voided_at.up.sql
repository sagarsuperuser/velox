-- Coupon redemption: voided_at for refund / full-credit reversal.
--
-- When an invoice with a coupon redemption is fully credited or refunded,
-- the redemption should stop contributing to future invoices on that
-- subscription and the coupon's usage counter should reflect that the
-- redemption didn't stick. Previously nothing reversed — a "once" coupon
-- stayed exhausted even after a full refund, and "times_redeemed" kept
-- counting refunded uses toward max_redemptions.
--
-- voided_at is set by coupon.Service.VoidRedemptionsForInvoice in the
-- same tx that decrements coupons.times_redeemed and rolls back
-- periods_applied (if the billing engine had already bumped it). The
-- partial index on the hot lookup — active redemptions for a subscription
-- — stays lean because voided rows drop out.

ALTER TABLE coupon_redemptions
    ADD COLUMN voided_at TIMESTAMPTZ;

-- Subscription scoped partial index: ApplyToInvoice scans this hot path
-- on every invoice generation, so excluding voided rows at the index
-- level avoids the table scan when a long-lived subscription accumulates
-- voided redemptions.
CREATE INDEX idx_coupon_redemptions_subscription_active
    ON coupon_redemptions (tenant_id, subscription_id)
    WHERE subscription_id IS NOT NULL AND voided_at IS NULL;
