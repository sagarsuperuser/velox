-- Reverse the source_change_type extension. Dedup indexes revert to their
-- 0026 shape (no change_type component), which means any rows with
-- change_type IN ('quantity','add','remove') would collide with plan-change
-- rows sharing the same (sub, item, timestamp). Acceptable on a rollback
-- because the roll-forward dataset wouldn't have produced such rows.

DROP INDEX IF EXISTS idx_credit_ledger_proration_dedup;
CREATE UNIQUE INDEX idx_credit_ledger_proration_dedup
    ON customer_credit_ledger (tenant_id, source_subscription_id, source_subscription_item_id, source_plan_changed_at)
    WHERE source_subscription_id IS NOT NULL AND source_plan_changed_at IS NOT NULL;

ALTER TABLE customer_credit_ledger
    DROP CONSTRAINT IF EXISTS credit_ledger_source_change_type_check;

ALTER TABLE customer_credit_ledger
    DROP COLUMN IF EXISTS source_change_type;

DROP INDEX IF EXISTS idx_invoices_proration_dedup;
CREATE UNIQUE INDEX idx_invoices_proration_dedup
    ON invoices (tenant_id, subscription_id, source_subscription_item_id, source_plan_changed_at)
    WHERE source_plan_changed_at IS NOT NULL;

ALTER TABLE invoices
    DROP CONSTRAINT IF EXISTS invoices_source_change_type_check;

ALTER TABLE invoices
    DROP COLUMN IF EXISTS source_change_type;
