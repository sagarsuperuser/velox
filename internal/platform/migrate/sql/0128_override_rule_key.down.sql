-- Reverse the rule_key keying. Demoted duplicate rows (active=false
-- from the up-migration's collision collapse) are NOT re-activated —
-- we can't distinguish them from rows the operator deactivated, and
-- re-activating would resurrect the pre-ADR-070 duplicate shape the
-- version-id unique below requires to be absent. Rows the collapse
-- demoted stay inactive; the recreated constraint is satisfied either
-- way because it spans ALL rows and the up-migration never inserted.
DROP INDEX IF EXISTS idx_price_overrides_active_rule_key;
-- The up-migration's collision collapse guarantees at most one ACTIVE
-- row per version, but append-only upserts made after the migration may
-- have created multiple INACTIVE rows for one (customer, version) —
-- collapse them to the newest so the version-id unique can be restored.
WITH ranked AS (
    SELECT id,
           ROW_NUMBER() OVER (
               PARTITION BY tenant_id, customer_id, rating_rule_version_id
               ORDER BY active DESC, updated_at DESC, id DESC
           ) AS rn
    FROM customer_price_overrides
)
DELETE FROM customer_price_overrides o
USING ranked r
WHERE o.id = r.id AND r.rn > 1;
ALTER TABLE customer_price_overrides
    ADD CONSTRAINT customer_price_overrides_tenant_id_customer_id_rating_rule__key
    UNIQUE (tenant_id, customer_id, rating_rule_version_id);
ALTER TABLE customer_price_overrides DROP COLUMN rule_key;
ALTER TABLE customer_price_overrides DROP COLUMN deactivated_at;
