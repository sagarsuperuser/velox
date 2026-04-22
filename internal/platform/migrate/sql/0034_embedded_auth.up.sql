-- Embedded email + password auth for dashboard users. Complements (does not
-- replace) API-key auth: bearer tokens still authenticate programmatic
-- clients; cookies authenticate UI users.
--
-- No RLS on any of these tables. Auth runs BEFORE tenant context is set —
-- it's what sets the tenant context — so tenant-scoped RLS would be
-- circular. Access is governed by TxBypass + application logic.
--
-- No livemode partitioning. A user's session holds `livemode` as the
-- *active view*, not as a row partition: toggling between live and test
-- mode updates the session row in place instead of spawning a parallel
-- session. Session rows span modes, so they stay out of the 0021 auto-set
-- trigger's registered table list.

ALTER TABLE users ADD COLUMN password_hash TEXT;
ALTER TABLE users ADD COLUMN email_verified_at TIMESTAMPTZ;

-- Many-to-many: a user can belong to multiple tenants; a tenant has many
-- users. Today the bootstrap CLI creates exactly one owner row, but the
-- junction exists so invites / multi-workspace land later without a
-- schema migration.
CREATE TABLE user_tenants (
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    tenant_id  TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    role       TEXT NOT NULL DEFAULT 'owner' CHECK (role IN ('owner', 'admin', 'member')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, tenant_id)
);

CREATE INDEX idx_user_tenants_tenant ON user_tenants(tenant_id);

-- Opaque server-side sessions. Cookie carries the raw session ID; the DB
-- stores sha256(id) so a DB snapshot can't be replayed as a bearer token.
-- Sessions rotate on login (prevents fixation) and on privilege-change.
CREATE TABLE sessions (
    id_hash      TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    tenant_id    TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    livemode     BOOLEAN NOT NULL DEFAULT false,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ NOT NULL,
    revoked_at   TIMESTAMPTZ,
    user_agent   TEXT,
    ip           TEXT
);

CREATE INDEX idx_sessions_user     ON sessions(user_id)    WHERE revoked_at IS NULL;
CREATE INDEX idx_sessions_expires  ON sessions(expires_at) WHERE revoked_at IS NULL;

-- Single-use short-TTL password-reset tokens. Stored as sha256(token) for
-- the same reason as sessions. The raw token is emailed once, compared by
-- hash on consumption, and marked consumed_at on first successful use.
CREATE TABLE password_reset_tokens (
    token_hash  TEXT PRIMARY KEY,
    user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at  TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_password_reset_tokens_user ON password_reset_tokens(user_id);
