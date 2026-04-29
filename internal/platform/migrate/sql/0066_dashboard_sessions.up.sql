-- Dashboard sessions: short-lived httpOnly cookies minted from a pasted
-- API key. The credential stays the API key (durable, operator-managed,
-- per project_auth_decision.md and ADR-007). Browser-side state is a
-- session cookie tied to that key — see ADR-008 for the rationale.
--
-- Why a separate table from the legacy `sessions` table from migration
-- 0034: that table was keyed off `users` (NOT NULL FK). With user
-- accounts gone (auth-revert, 2026-04-29) the FK shape no longer
-- matches. Rather than alter the legacy table in place we leave it
-- untouched and add this one. Callers reach the new table via
-- `internal/session/`.
--
-- Cookie carries the raw session id (256-bit random); the DB stores
-- sha256(id) so a DB snapshot can't be replayed as a bearer token.
-- Sessions auto-expire and can be revoked server-side, which is the
-- whole point of this table over Bearer-in-localStorage.

CREATE TABLE dashboard_sessions (
    id_hash      TEXT PRIMARY KEY,
    key_id       TEXT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    tenant_id    TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    livemode     BOOLEAN NOT NULL DEFAULT false,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ NOT NULL,
    revoked_at   TIMESTAMPTZ,
    user_agent   TEXT,
    ip           TEXT
);

-- Lookup index for active sessions per key (used during key revocation
-- to mass-revoke any cookie minted from it).
CREATE INDEX idx_dashboard_sessions_key
    ON dashboard_sessions (key_id)
    WHERE revoked_at IS NULL;

-- Cleanup index for the periodic expired-session sweep.
CREATE INDEX idx_dashboard_sessions_expires
    ON dashboard_sessions (expires_at)
    WHERE revoked_at IS NULL;
