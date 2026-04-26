DROP INDEX IF EXISTS idx_webhook_events_replay_of;
ALTER TABLE webhook_events DROP COLUMN IF EXISTS replay_of_event_id;
