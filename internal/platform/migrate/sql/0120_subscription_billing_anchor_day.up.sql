-- Persist the operator's intended billing anchor day-of-month (1..31) so
-- anniversary and yearly subscriptions clamp to month-end correctly instead of
-- ratcheting off the previously-computed boundary via Go's AddDate overflow.
-- A Jan-31 anniversary must bill Jan 31, Feb 28, Mar 31, Apr 30, … (Stripe /
-- Chargebee / Lago parity) — NOT Jan 31, Mar 3, Apr 3, … locked on the 3rd
-- forever. The pre-fix advance was self-referential (periodEnd + 1 month off
-- the already-drifted end), so the original anchor day cannot be recovered and
-- must be stored. ADR-055 (supersedes the ADR-058 §"not a bug" note).
--
-- 0 = unset/legacy → the engine preserves the historical addIntervalIn path
-- (no behavior change) so this column is additive and safe.
ALTER TABLE subscriptions
    ADD COLUMN billing_anchor_day SMALLINT NOT NULL DEFAULT 0;

-- Best-effort backfill from the current period start's day-of-month, gated to
-- ANNIVERSARY subs only — matching domain.AnchorDayFor, which returns 0 for
-- calendar-monthly subs (their boundary is always the 1st, so a non-zero anchor
-- would needlessly perturb the proration denominator on a high-day stub row).
-- Velox is pre-launch / local-only (no production rows); new subs set this
-- precisely at activation in the tenant timezone, and the rare calendar-yearly
-- legacy row re-anchors at its next activation.
UPDATE subscriptions
    SET billing_anchor_day = EXTRACT(DAY FROM current_billing_period_start)::smallint
    WHERE billing_time = 'anniversary' AND current_billing_period_start IS NOT NULL;
