-- Move stripe_customer_id from customer_payment_setups (1:1 denorm
-- summary) to customers (the authoritative customer row). The summary
-- table was three concerns smashed together: Stripe Customer mapping,
-- default-PM cache, and setup-status state. With payment_methods as
-- the canonical multi-PM store (since migration 0022) and setup_status
-- meaningless under multi-PM, the only durable thing left in the
-- summary is the Stripe Customer ID — which belongs on the customer
-- record itself.
--
-- After this migration:
--   - customers.stripe_customer_id is the single source of truth for
--     "this Velox customer maps to this Stripe Customer object"
--   - payment_methods is the canonical multi-PM store (one row per
--     card, is_default flag tracks the primary)
--   - customer_payment_setups is deprecated; migration 0097 drops it
--     after all code paths are migrated off it
--
-- Idempotent backfill: only writes when the customers row doesn't
-- already have a stripe_customer_id, so re-running is safe.

ALTER TABLE customers
    ADD COLUMN IF NOT EXISTS stripe_customer_id TEXT;

-- Backfill from the legacy summary table. Match on (tenant_id,
-- customer_id) which is the PK of customer_payment_setups.
UPDATE customers c
   SET stripe_customer_id = cps.stripe_customer_id
  FROM customer_payment_setups cps
 WHERE cps.tenant_id = c.tenant_id
   AND cps.customer_id = c.id
   AND cps.stripe_customer_id IS NOT NULL
   AND cps.stripe_customer_id != ''
   AND (c.stripe_customer_id IS NULL OR c.stripe_customer_id = '');

-- Stripe Customer IDs are globally unique inside a Stripe account, so
-- per-(tenant, livemode) uniqueness is the right scope. Partial unique
-- index — NULL values don't collide.
CREATE UNIQUE INDEX IF NOT EXISTS idx_customers_stripe_customer_id_unique
    ON customers (tenant_id, livemode, stripe_customer_id)
    WHERE stripe_customer_id IS NOT NULL AND stripe_customer_id != '';
