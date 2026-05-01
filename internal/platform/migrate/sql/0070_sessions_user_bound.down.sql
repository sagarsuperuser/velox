-- Rolls back the user-bound session shape. Sessions become key-bound
-- again. Existing user-bound rows are dropped (no backfill path —
-- ADR-011 deliberately did a hard cutover).

DELETE FROM dashboard_sessions;
DROP INDEX IF EXISTS idx_dashboard_sessions_user;

ALTER TABLE dashboard_sessions
    DROP COLUMN user_id,
    ADD COLUMN key_id TEXT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE;

CREATE INDEX idx_dashboard_sessions_key
    ON dashboard_sessions (key_id)
    WHERE revoked_at IS NULL;
