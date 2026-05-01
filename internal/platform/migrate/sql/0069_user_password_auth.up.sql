-- ADR-011: email/password dashboard auth. Adds the user-side of the
-- auth model that's been missing since the ADR-007 revert. Dashboard
-- sessions become user-bound (see 0070); API keys become pure SDK
-- credentials with the safeguard subsystem deleted.
--
-- Three tables:
--   users                  — operator accounts; email is the login key
--   user_tenants           — many-to-many; in v1 always one row per
--                            user with role='owner', shape leaves room
--                            for multi-tenant + invite flows later
--   password_reset_tokens  — single-use, 1h-expiry tokens; plaintext
--                            sent only via email link, hashed in DB
--
-- CITEXT for email so login is case-insensitive without a LOWER index
-- — pg_trgm-style indexes on a CITEXT column work normally.

CREATE EXTENSION IF NOT EXISTS citext;

CREATE TABLE users (
    id            TEXT PRIMARY KEY DEFAULT 'vlx_usr_' || encode(gen_random_bytes(12), 'hex'),
    email         CITEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_login_at TIMESTAMPTZ,
    -- locked_until = now() + 15min after 5 failed logins (login rate
    -- limiter sets this; auth handler refuses login until cleared).
    locked_until  TIMESTAMPTZ
);

CREATE TABLE user_tenants (
    user_id   TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    role      TEXT NOT NULL DEFAULT 'owner' CHECK (role IN ('owner')),
    PRIMARY KEY (user_id, tenant_id)
);

CREATE INDEX idx_user_tenants_tenant ON user_tenants (tenant_id);

CREATE TABLE password_reset_tokens (
    id         TEXT PRIMARY KEY DEFAULT 'vlx_prt_' || encode(gen_random_bytes(12), 'hex'),
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    used_at    TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_password_reset_tokens_user ON password_reset_tokens (user_id);
