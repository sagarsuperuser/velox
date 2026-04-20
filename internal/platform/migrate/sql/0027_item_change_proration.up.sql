-- FEAT-5 follow-up: mid-cycle quantity / add / remove proration. Before this,
-- only plan changes triggered per-item proration — qty changes updated the
-- row silently, item adds billed full period at next cycle, removes vanished
-- with no credit. All three now land on the same handleItemProration path.
--
-- The dedup key must distinguish concurrent mutations on the same item that
-- coincidentally share a wall-clock instant (e.g. a plan change and a qty
-- change within the same microsecond). Without a change_type discriminator
-- the unique index collapses them into one invoice and silently swallows the
-- second proration. Adding source_change_type makes each mutation stand on
-- its own dedup tuple.
--
-- source_plan_changed_at keeps its name (vs. rename to source_item_change_at)
-- because renaming touches every proration read/write path — the migration
-- surface is already large, and the name is semantically wider than it reads
-- (stamps any item change). A dedicated cleanup migration can rename later.

-- ===========================================================================
-- 1. Extend invoices dedup key
-- ===========================================================================
ALTER TABLE invoices
    ADD COLUMN source_change_type TEXT;

-- Existing plan-change invoices backfill to 'plan' — before this migration
-- the only path that set source_plan_changed_at was ApplyItemPlanImmediately.
UPDATE invoices
    SET source_change_type = 'plan'
    WHERE source_plan_changed_at IS NOT NULL;

ALTER TABLE invoices
    ADD CONSTRAINT invoices_source_change_type_check
    CHECK (source_change_type IS NULL OR source_change_type IN ('plan', 'quantity', 'add', 'remove'));

-- Dedup tuple now includes change_type so (plan@T, quantity@T) are distinct.
-- Partial predicate still gates on source_plan_changed_at (the column that
-- every proration path writes) — source_change_type tracks it but adding a
-- second non-null test would be redundant.
DROP INDEX IF EXISTS idx_invoices_proration_dedup;
CREATE UNIQUE INDEX idx_invoices_proration_dedup
    ON invoices (tenant_id, subscription_id, source_subscription_item_id, source_change_type, source_plan_changed_at)
    WHERE source_plan_changed_at IS NOT NULL;

-- ===========================================================================
-- 2. Extend customer_credit_ledger dedup key
-- ===========================================================================
ALTER TABLE customer_credit_ledger
    ADD COLUMN source_change_type TEXT;

UPDATE customer_credit_ledger
    SET source_change_type = 'plan'
    WHERE source_plan_changed_at IS NOT NULL;

ALTER TABLE customer_credit_ledger
    ADD CONSTRAINT credit_ledger_source_change_type_check
    CHECK (source_change_type IS NULL OR source_change_type IN ('plan', 'quantity', 'add', 'remove'));

DROP INDEX IF EXISTS idx_credit_ledger_proration_dedup;
CREATE UNIQUE INDEX idx_credit_ledger_proration_dedup
    ON customer_credit_ledger (tenant_id, source_subscription_id, source_subscription_item_id, source_change_type, source_plan_changed_at)
    WHERE source_subscription_id IS NOT NULL AND source_plan_changed_at IS NOT NULL;
