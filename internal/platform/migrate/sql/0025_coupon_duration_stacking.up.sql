-- FEAT-6: coupon duration + stacking semantics.
--
-- duration is Stripe-style:
--   once       — applies to the first eligible invoice only
--   repeating  — applies for N billing periods (duration_periods)
--   forever    — applies to every invoice while the redemption is active
--
-- duration_periods is meaningful only for duration='repeating'. The CHECK
-- keeps the two fields consistent so a 'repeating' row can't exist without
-- a positive period count, and 'once'/'forever' rows can't smuggle a
-- period count that would later confuse the apply logic.
--
-- stackable opts the coupon into combining with other stackable coupons on
-- the same subscription. The default is false so existing coupons retain
-- the current "best single coupon per invoice" behaviour. When any non-
-- stackable coupon is present on a subscription, it wins alone; stackables
-- combine only when every applicable coupon is stackable.
--
-- coupon_redemptions.periods_applied tracks how many billing cycles have
-- already consumed this redemption. The billing engine increments it after
-- an invoice that used the discount commits — repeating coupons exhaust
-- when periods_applied reaches the coupon's duration_periods, once coupons
-- exhaust at 1.

ALTER TABLE coupons
    ADD COLUMN duration TEXT NOT NULL DEFAULT 'forever'
        CHECK (duration IN ('once', 'repeating', 'forever')),
    ADD COLUMN duration_periods INT,
    ADD COLUMN stackable BOOLEAN NOT NULL DEFAULT false;

ALTER TABLE coupons
    ADD CONSTRAINT coupons_duration_periods_check CHECK (
        (duration = 'repeating' AND duration_periods IS NOT NULL AND duration_periods >= 1)
        OR (duration IN ('once', 'forever') AND duration_periods IS NULL)
    );

ALTER TABLE coupon_redemptions
    ADD COLUMN periods_applied INT NOT NULL DEFAULT 0
        CHECK (periods_applied >= 0);
