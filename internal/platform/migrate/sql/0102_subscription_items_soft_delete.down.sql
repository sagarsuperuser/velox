-- Restore the prior hard-delete model. Rolling back is destructive —
-- soft-deleted rows must be physically purged first, or the
-- (subscription_id, plan_id) UNIQUE will fail on rebuild.
--
-- Operator must run `DELETE FROM subscription_items WHERE deleted_at
-- IS NOT NULL;` BEFORE applying this down migration. The script
-- doesn't auto-purge to keep the rollback explicit.

DROP INDEX IF EXISTS idx_subscription_items_live;
DROP INDEX IF EXISTS subscription_items_subscription_id_plan_id_key;
ALTER TABLE subscription_items
    ADD CONSTRAINT subscription_items_subscription_id_plan_id_key
    UNIQUE (subscription_id, plan_id);

ALTER TABLE subscription_items
    DROP COLUMN IF EXISTS deleted_at;
