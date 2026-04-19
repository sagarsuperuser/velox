DROP INDEX IF EXISTS idx_subscriptions_pending_plan_due;

ALTER TABLE subscriptions
    DROP COLUMN IF EXISTS pending_plan_effective_at,
    DROP COLUMN IF EXISTS pending_plan_id;
