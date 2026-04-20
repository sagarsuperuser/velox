-- FEAT-3 P4.5: single-use magic-link tokens for customer-initiated portal
-- access. Separate table from customer_portal_sessions because the
-- semantics differ:
--
--   customer_portal_sessions   — reusable within TTL (1h), revocable.
--   customer_portal_magic_links — single-use (used_at locks the row),
--                                 short TTL (15 min) to minimise exposure
--                                 window if an email is intercepted.
--
-- Consuming a magic link (GET /v1/public/customer-portal/magic/{token})
-- atomically marks used_at and mints a customer_portal_sessions row that
-- the customer's browser then uses for subsequent /v1/me/* calls.

CREATE TABLE customer_portal_magic_links (
    id          TEXT PRIMARY KEY DEFAULT 'vlx_cpml_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id   TEXT NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
    livemode    BOOLEAN NOT NULL DEFAULT true,
    customer_id TEXT NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    -- sha256(raw_token) — raw form returned to the email body exactly once
    -- at create time and never persisted, matching payment_update_tokens.
    token_hash  TEXT NOT NULL UNIQUE,
    expires_at  TIMESTAMPTZ NOT NULL,
    used_at     TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Lookup by (tenant, customer, still-valid) for the rate-limit check —
-- "does this customer already have an unused, unexpired link?" — so we
-- don't mint a fresh token on every keystroke of the login form.
CREATE INDEX idx_customer_portal_magic_links_customer
    ON customer_portal_magic_links (tenant_id, livemode, customer_id, created_at DESC)
    WHERE used_at IS NULL;

-- Cleanup sweeper targets expired rows via this index.
CREATE INDEX idx_customer_portal_magic_links_expires
    ON customer_portal_magic_links (expires_at)
    WHERE used_at IS NULL;

ALTER TABLE customer_portal_magic_links ENABLE ROW LEVEL SECURITY;
ALTER TABLE customer_portal_magic_links FORCE  ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON customer_portal_magic_links FOR ALL USING (
    current_setting('app.bypass_rls', true) = 'on'
    OR (
        tenant_id = current_setting('app.tenant_id', true)
        AND livemode = (current_setting('app.livemode', true) IS DISTINCT FROM 'off')
    )
);

GRANT ALL ON TABLE customer_portal_magic_links TO velox_app;

CREATE TRIGGER set_livemode
    BEFORE INSERT ON customer_portal_magic_links
    FOR EACH ROW EXECUTE FUNCTION set_livemode_from_session();
