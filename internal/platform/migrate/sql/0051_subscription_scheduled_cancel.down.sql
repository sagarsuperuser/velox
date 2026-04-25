DROP INDEX IF EXISTS idx_subscriptions_cancel_at;
ALTER TABLE subscriptions DROP COLUMN IF EXISTS cancel_at;
ALTER TABLE subscriptions DROP COLUMN IF EXISTS cancel_at_period_end;
