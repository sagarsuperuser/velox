-- Reverse migration 0108 — narrows the per-unit rate columns back to BIGINT.
--
-- LOSSY by nature: any sub-cent fractional rate written while the column was
-- NUMERIC rounds to the nearest whole cent on the way down (round() then
-- ::bigint). This is inherent to reverting a precision upgrade — the int64
-- form simply cannot hold 0.0003. Pre-launch (no production rate data), so
-- acceptable; restoring the original BIGINT type keeps earlier migrations'
-- downs valid.

ALTER TABLE rating_rule_versions
    ALTER COLUMN flat_amount_cents TYPE BIGINT USING round(flat_amount_cents)::bigint,
    ALTER COLUMN overage_unit_amount_cents TYPE BIGINT USING round(overage_unit_amount_cents)::bigint;

ALTER TABLE customer_price_overrides
    ALTER COLUMN flat_amount_cents TYPE BIGINT USING round(flat_amount_cents)::bigint,
    ALTER COLUMN overage_unit_amount_cents TYPE BIGINT USING round(overage_unit_amount_cents)::bigint;
