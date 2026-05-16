-- Reverse ADR-036.
--
-- customer_dunning_overrides is re-created with its original shape.
-- Existing per-customer dunning_policy_id assignments are NOT migrated
-- back into override rows — the down-migration is best-effort; the
-- forward path was the design decision and reverting is for emergency
-- rollback only.

-- 1. Drop the per-customer policy FK + index.
DROP INDEX IF EXISTS idx_customers_dunning_policy_id;
ALTER TABLE customers DROP COLUMN IF EXISTS dunning_policy_id;

-- 2. dunning_policies: drop is_default + restore the singleton-per-
--    tenant UNIQUE constraint. If multiple rows exist per tenant the
--    constraint addition will fail; the operator must consolidate
--    first.
DROP INDEX IF EXISTS idx_dunning_policies_one_default_per_tenant;
ALTER TABLE dunning_policies DROP COLUMN IF EXISTS is_default;
ALTER TABLE dunning_policies
  ADD CONSTRAINT dunning_policies_tenant_id_livemode_key
  UNIQUE (tenant_id, livemode);

-- 3. Re-create customer_dunning_overrides (shape from pre-ADR-036).
CREATE TABLE IF NOT EXISTS customer_dunning_overrides (
  customer_id        TEXT NOT NULL,
  tenant_id          TEXT NOT NULL,
  max_retry_attempts INTEGER,
  grace_period_days  INTEGER,
  final_action       TEXT,
  created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  livemode           BOOLEAN NOT NULL DEFAULT true,
  PRIMARY KEY (customer_id, tenant_id)
);
