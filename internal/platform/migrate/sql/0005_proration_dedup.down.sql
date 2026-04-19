DROP INDEX IF EXISTS idx_credit_ledger_proration_dedup;
ALTER TABLE customer_credit_ledger
    DROP COLUMN IF EXISTS source_subscription_id,
    DROP COLUMN IF EXISTS source_plan_changed_at;

DROP INDEX IF EXISTS idx_invoices_proration_dedup;
ALTER TABLE invoices DROP COLUMN IF EXISTS source_plan_changed_at;
