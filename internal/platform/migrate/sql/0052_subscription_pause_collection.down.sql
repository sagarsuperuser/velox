DROP INDEX IF EXISTS idx_subscriptions_pause_collection_resumes_at;
ALTER TABLE subscriptions
    DROP CONSTRAINT IF EXISTS subscriptions_pause_collection_behavior_check,
    DROP COLUMN IF EXISTS pause_collection_resumes_at,
    DROP COLUMN IF EXISTS pause_collection_behavior;
