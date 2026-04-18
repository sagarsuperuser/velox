-- User accounts for dashboard login
CREATE TABLE user_accounts (
    id              TEXT PRIMARY KEY DEFAULT 'vlx_usr_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id       TEXT NOT NULL REFERENCES tenants(id),
    email           TEXT NOT NULL,
    password_hash   TEXT NOT NULL,
    name            TEXT NOT NULL DEFAULT '',
    role            TEXT NOT NULL DEFAULT 'admin' CHECK (role IN ('owner', 'admin', 'member')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, email)
);

ALTER TABLE user_accounts ENABLE ROW LEVEL SECURITY;
ALTER TABLE user_accounts FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON user_accounts FOR ALL USING (
    current_setting('app.bypass_rls', true) = 'on' OR tenant_id = current_setting('app.tenant_id', true)
);
GRANT ALL ON TABLE user_accounts TO velox_app;

-- Sessions for cookie-based auth
CREATE TABLE user_sessions (
    id              TEXT PRIMARY KEY DEFAULT 'vlx_ses_' || encode(gen_random_bytes(12), 'hex'),
    user_id         TEXT NOT NULL REFERENCES user_accounts(id) ON DELETE CASCADE,
    tenant_id       TEXT NOT NULL REFERENCES tenants(id),
    token_hash      TEXT NOT NULL,
    expires_at      TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_sessions_token ON user_sessions (token_hash);
CREATE INDEX idx_sessions_expiry ON user_sessions (expires_at);
GRANT ALL ON TABLE user_sessions TO velox_app;

-- Password reset tokens
CREATE TABLE password_reset_tokens (
    id              TEXT PRIMARY KEY DEFAULT 'vlx_prt_' || encode(gen_random_bytes(12), 'hex'),
    user_id         TEXT NOT NULL REFERENCES user_accounts(id) ON DELETE CASCADE,
    token_hash      TEXT NOT NULL,
    expires_at      TIMESTAMPTZ NOT NULL,
    used_at         TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_reset_tokens_hash ON password_reset_tokens (token_hash);
GRANT ALL ON TABLE password_reset_tokens TO velox_app;
