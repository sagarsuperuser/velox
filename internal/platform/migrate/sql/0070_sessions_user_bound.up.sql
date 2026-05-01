-- ADR-011: dashboard sessions become user-bound, not key-bound.
-- The session cookie is no longer minted by exchanging an API key
-- (`POST /v1/auth/exchange`); it's minted by `POST /v1/auth/login`
-- with email + password. So the session row's foreign key shifts
-- from `api_keys` to `users`.
--
-- Because Velox is pre-launch with one operator and no production
-- traffic, we don't backfill: existing dashboard_sessions rows get
-- DROP'd and the operator re-bootstraps + signs in via the new
-- email+password flow. See ADR-011.
--
-- Live livemode column stays (cookie-side mode tracking is still
-- useful for live/test dashboard segregation if a tenant ever ends
-- up authenticated against the wrong mode).

DELETE FROM dashboard_sessions;
DROP INDEX IF EXISTS idx_dashboard_sessions_key;

ALTER TABLE dashboard_sessions
    DROP COLUMN key_id,
    ADD COLUMN user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE;

CREATE INDEX idx_dashboard_sessions_user
    ON dashboard_sessions (user_id)
    WHERE revoked_at IS NULL;
