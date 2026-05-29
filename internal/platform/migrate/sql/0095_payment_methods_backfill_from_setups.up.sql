-- Backfill payment_methods rows for customers whose card was attached
-- via the legacy /v1/checkout/setup flow (which only wrote to the 1:1
-- customer_payment_setups summary). The new portal /v1/me/payment-
-- methods endpoint reads from the canonical payment_methods table —
-- so without this backfill the portal renders "No card on file" even
-- though billing's auto-charge path keeps working (it reads the
-- summary).
--
-- Idempotent: only inserts when no payment_methods row exists for
-- (tenant, livemode, customer). Re-running is safe — already-migrated
-- customers are skipped.
--
-- Forward-compat: all new card attachments go through
-- paymentmethods.Service.AttachForWebhook (wired into
-- setup_intent.succeeded) which writes BOTH tables. This migration
-- only catches the historical drift.

INSERT INTO payment_methods (
    id,
    tenant_id,
    livemode,
    customer_id,
    stripe_payment_method_id,
    type,
    card_brand,
    card_last4,
    card_exp_month,
    card_exp_year,
    is_default,
    created_at,
    updated_at
)
SELECT
    'vlx_pm_' || encode(gen_random_bytes(12), 'hex'),
    cps.tenant_id,
    cps.livemode,
    cps.customer_id,
    cps.stripe_payment_method_id,
    COALESCE(NULLIF(cps.payment_method_type, ''), 'card'),
    cps.card_brand,
    cps.card_last4,
    cps.card_exp_month,
    cps.card_exp_year,
    cps.default_payment_method_present,
    COALESCE(cps.last_verified_at, cps.created_at, now()),
    cps.updated_at
FROM customer_payment_setups cps
WHERE cps.setup_status = 'ready'
  AND cps.stripe_payment_method_id IS NOT NULL
  AND cps.stripe_payment_method_id != ''
  AND NOT EXISTS (
      SELECT 1
      FROM payment_methods pm
      WHERE pm.tenant_id = cps.tenant_id
        AND pm.livemode = cps.livemode
        AND pm.stripe_payment_method_id = cps.stripe_payment_method_id
  );
