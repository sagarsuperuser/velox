-- Migration 0108: per-unit pricing rate precision (ADR-044 follow-up).
--
-- Per-unit rate fields were INT64 cents, so a rate is at best whole-cent
-- granularity. AI token pricing needs sub-cent-per-unit rates: $3.00 per
-- 1,000,000 tokens = 0.0003 cents/token. With integer cents the canonical
-- anthropic_style recipe mis-modeled this — flat_amount_cents=300 in flat
-- (per-unit) mode bills $3.00 PER TOKEN, a 1,000,000x overcharge.
--
-- Industry peers price sub-cent usage with decimal unit prices: Stripe's
-- `unit_amount_decimal` (string, up to 12 decimal places), Orb / Metronome /
-- Lago all carry high-precision decimal rates. This migration widens the
-- PER-UNIT rate columns (multiplied by a decimal quantity) to NUMERIC so
-- rates bill linearly and exactly.
--
-- IN SCOPE (per-unit rates): flat_amount_cents, overage_unit_amount_cents,
--   and graduated_tiers[].unit_amount_cents (JSONB — see note).
-- OUT OF SCOPE (fixed amounts, stay BIGINT): package_amount_cents (fixed
--   per-block fee), all invoice line amounts and totals, tax, credits. You
--   still cannot charge a customer a fractional cent — only the RATE gains
--   precision; line amounts/totals round to whole int64 cents at the end.
--
-- NUMERIC (unbounded) chosen over a fixed scale: rates span large flat fees
-- (former BIGINT range) AND sub-cent token prices in one column, and
-- shopspring/decimal round-trips arbitrary precision losslessly.
--
-- graduated_tiers is JSONB; its nested unit_amount_cents needs no DDL. New
-- writes marshal the decimal as a JSON string ("0.0003"); decimal's
-- UnmarshalJSON reads both the legacy numeric form and the string form, so
-- existing rows keep working without a data transform.

ALTER TABLE rating_rule_versions
    ALTER COLUMN flat_amount_cents TYPE NUMERIC USING flat_amount_cents::numeric,
    ALTER COLUMN overage_unit_amount_cents TYPE NUMERIC USING overage_unit_amount_cents::numeric;

ALTER TABLE customer_price_overrides
    ALTER COLUMN flat_amount_cents TYPE NUMERIC USING flat_amount_cents::numeric,
    ALTER COLUMN overage_unit_amount_cents TYPE NUMERIC USING overage_unit_amount_cents::numeric;
