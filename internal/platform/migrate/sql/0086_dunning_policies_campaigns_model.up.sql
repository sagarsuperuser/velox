-- ADR-036: dunning campaigns model (multi-policy-per-tenant + per-customer
-- assignment). Aligns Velox with the converging shape used by Stripe,
-- Lago, Orb, and Recurly: named templates that customers reference.
--
-- Schema changes:
--   1. dunning_policies: allow multiple per tenant; add is_default
--      flag (exactly one default per tenant+livemode).
--   2. customers: add dunning_policy_id nullable FK — nil = use the
--      tenant's default policy.
--   3. drop customer_dunning_overrides — partial-field override has no
--      industry precedent and produced ambiguous behaviour (silent
--      retry-schedule fallback). Replaced by full policy assignment.
--
-- Velox is pre-launch (zero design partners, one user account per
-- project_state_2026_04), so no operator-visible data to preserve in
-- customer_dunning_overrides.

-- 1. dunning_policies → multi-per-tenant.
--
--    Pre-fix the unique constraint enforced singleton-per-tenant
--    (tenant_id + livemode key). Drop it; allow multiple rows.
ALTER TABLE dunning_policies
  DROP CONSTRAINT IF EXISTS dunning_policies_tenant_id_livemode_key;

--    is_default marks the policy used when a customer has no explicit
--    assignment. Default false on the column so new rows opt in
--    explicitly; backfill the singleton existing row(s) to true.
ALTER TABLE dunning_policies
  ADD COLUMN IF NOT EXISTS is_default BOOLEAN NOT NULL DEFAULT false;

UPDATE dunning_policies SET is_default = true;

--    Exactly one default per (tenant_id, livemode). Partial unique
--    index gates the invariant at the DB level — application bugs
--    can't drift the data into "two defaults" or "zero defaults"
--    states.
CREATE UNIQUE INDEX IF NOT EXISTS idx_dunning_policies_one_default_per_tenant
  ON dunning_policies (tenant_id, livemode)
  WHERE is_default;

-- 2. customers.dunning_policy_id — nullable FK to dunning_policies.id.
--
--    Nullable = "use tenant default." ON DELETE SET NULL so deleting
--    a policy un-assigns customers cleanly rather than blocking. The
--    application layer additionally refuses to delete the default
--    policy and refuses to delete a policy if any customer references
--    it explicitly (validated in dunning service); the SET NULL is a
--    belt-and-suspenders backstop against bypasses.
ALTER TABLE customers
  ADD COLUMN IF NOT EXISTS dunning_policy_id TEXT
    REFERENCES dunning_policies(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_customers_dunning_policy_id
  ON customers (tenant_id, dunning_policy_id)
  WHERE dunning_policy_id IS NOT NULL;

-- 3. Drop customer_dunning_overrides (and its dependent constraints).
--
--    Note: invoice_dunning_runs has no FK to this table, so no cascade
--    risk. Confirmed by `\d invoice_dunning_runs` audit — runs
--    reference the policy via run.policy_id (FK to dunning_policies),
--    not via the override table.
DROP TABLE IF EXISTS customer_dunning_overrides;
