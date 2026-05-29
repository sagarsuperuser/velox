-- Add card_fingerprint for Stripe-recommended dedupe-by-fingerprint.
-- Stripe mints a new PaymentMethod object every time a customer
-- completes Checkout, even when the same physical card number is
-- used. Without dedupe, the portal renders the same Visa-4242 twice
-- (or more) — confusing UX and a stale-PM hazard for billing.
--
-- The fingerprint is a Stripe-provided stable hash of the card
-- number (CVC + expiry don't affect it). Industry standard for
-- detecting "same card, different PM ID": Stripe explicitly
-- recommends this for app-side dedupe.
--
-- Column is NULLABLE so legacy rows (attached before this
-- migration) don't need backfilling at deploy time. The unique
-- partial index also skips NULLs, so legacy rows coexist with new
-- ones until a customer re-adds (at which point dedupe collapses
-- them).
ALTER TABLE payment_methods
    ADD COLUMN card_fingerprint TEXT;

-- Partial index supports the dedupe lookup in Upsert: one active
-- (non-detached) row per (tenant, livemode, customer, fingerprint).
-- Detached rows + NULL-fingerprint rows are excluded so the index
-- doesn't grow unboundedly with churn and legacy data doesn't
-- collide.
CREATE UNIQUE INDEX idx_payment_methods_active_fingerprint
    ON payment_methods (tenant_id, livemode, customer_id, card_fingerprint)
    WHERE detached_at IS NULL AND card_fingerprint IS NOT NULL;
