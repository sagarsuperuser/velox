-- Reverse FEAT-5's multi-item subscription migration. Down recreates the
-- pre-FEAT-5 column shape on subscriptions and tears down subscription_items,
-- but does NOT reconstruct per-subscription plan_id values — doing so from N
-- items is ambiguous (which item wins?) and would silently corrupt the dataset.
-- Callers rolling back must re-seed subscriptions from their own source of
-- truth or re-run app bootstrap.

-- ===========================================================================
-- 1. Restore plan-scoped columns on subscriptions
-- ===========================================================================
ALTER TABLE subscriptions
    ADD COLUMN plan_id                   TEXT REFERENCES plans(id) ON DELETE RESTRICT,
    ADD COLUMN previous_plan_id          TEXT,
    ADD COLUMN plan_changed_at           TIMESTAMPTZ,
    ADD COLUMN pending_plan_id           TEXT REFERENCES plans(id) ON DELETE RESTRICT,
    ADD COLUMN pending_plan_effective_at TIMESTAMPTZ;

-- Restore the pending-change due index that moved to items.
CREATE INDEX IF NOT EXISTS idx_subscriptions_pending_plan_due
    ON subscriptions (pending_plan_effective_at)
    WHERE pending_plan_id IS NOT NULL;

-- Restore HYG-2's partial UNIQUE. Safe to recreate because the column is
-- nullable — no rows will match the partial predicate until plan_id is
-- backfilled, at which point existing uniqueness enforcement kicks in.
CREATE UNIQUE INDEX IF NOT EXISTS subscriptions_one_live_per_customer_plan
    ON subscriptions (tenant_id, customer_id, plan_id)
    WHERE status IN ('active', 'paused');

-- ===========================================================================
-- 2. Revert proration dedup keys
-- ===========================================================================
DROP INDEX IF EXISTS idx_credit_ledger_proration_dedup;
CREATE UNIQUE INDEX idx_credit_ledger_proration_dedup
    ON customer_credit_ledger (tenant_id, source_subscription_id, source_plan_changed_at)
    WHERE source_subscription_id IS NOT NULL AND source_plan_changed_at IS NOT NULL;

ALTER TABLE customer_credit_ledger
    DROP COLUMN IF EXISTS source_subscription_item_id;

DROP INDEX IF EXISTS idx_invoices_proration_dedup;
CREATE UNIQUE INDEX idx_invoices_proration_dedup
    ON invoices (tenant_id, subscription_id, source_plan_changed_at)
    WHERE source_plan_changed_at IS NOT NULL;

ALTER TABLE invoices
    DROP COLUMN IF EXISTS source_subscription_item_id;

-- ===========================================================================
-- 3. Drop subscription_items
-- ===========================================================================
DROP TRIGGER IF EXISTS set_livemode ON subscription_items;
DROP POLICY IF EXISTS tenant_isolation ON subscription_items;
DROP TABLE IF EXISTS subscription_items;
