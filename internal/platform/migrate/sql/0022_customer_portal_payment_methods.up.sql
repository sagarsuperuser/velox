-- FEAT-3: customer self-service payment methods.
--
-- Introduces two tables:
--   1. payment_methods    — multi-row per customer; one flagged as default.
--                           customer_payment_setups (0001) stays as a 1:1
--                           denorm summary populated from the default PM.
--   2. customer_portal_sessions — short-lived bearer tokens that authenticate
--                           a customer (not an operator) against /v1/me/*.
--                           Analogous to payment_update_tokens (0009) but
--                           customer-scoped rather than invoice-scoped and
--                           reusable within TTL rather than single-use.
--
-- Both tables are mode-aware (livemode column) and covered by the same RLS
-- predicate the rest of the schema uses. The 0021 BEFORE INSERT trigger is
-- attached inline for each so producers don't need to remember the column.

-- ---------------------------------------------------------------------------
-- 1. payment_methods
-- ---------------------------------------------------------------------------
CREATE TABLE payment_methods (
    id                       TEXT PRIMARY KEY DEFAULT 'vlx_pm_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id                TEXT NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
    livemode                 BOOLEAN NOT NULL DEFAULT true,
    customer_id              TEXT NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    stripe_payment_method_id TEXT NOT NULL,
    type                     TEXT NOT NULL DEFAULT 'card',
    card_brand               TEXT,
    card_last4               TEXT,
    card_exp_month           INT,
    card_exp_year            INT,
    is_default               BOOLEAN NOT NULL DEFAULT false,
    detached_at              TIMESTAMPTZ,
    created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- stripe_payment_method_id uniquely identifies a Stripe PM, so it should
    -- appear at most once per (tenant, mode). A PM attached to the wrong
    -- customer within the same tenant is a bug, not a re-use case.
    UNIQUE (tenant_id, livemode, stripe_payment_method_id)
);

-- At most one active default per customer per mode. A detached PM keeps its
-- is_default flag for audit but is excluded from the unique index, so a
-- new default can be promoted without UPDATE of the old row.
CREATE UNIQUE INDEX uniq_payment_methods_default_per_customer
    ON payment_methods (tenant_id, livemode, customer_id)
    WHERE is_default = true AND detached_at IS NULL;

CREATE INDEX idx_payment_methods_customer
    ON payment_methods (tenant_id, livemode, customer_id, detached_at);

ALTER TABLE payment_methods ENABLE ROW LEVEL SECURITY;
ALTER TABLE payment_methods FORCE  ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON payment_methods FOR ALL USING (
    current_setting('app.bypass_rls', true) = 'on'
    OR (
        tenant_id = current_setting('app.tenant_id', true)
        AND livemode = (current_setting('app.livemode', true) IS DISTINCT FROM 'off')
    )
);

GRANT ALL ON TABLE payment_methods TO velox_app;

CREATE TRIGGER set_livemode
    BEFORE INSERT ON payment_methods
    FOR EACH ROW EXECUTE FUNCTION set_livemode_from_session();

-- ---------------------------------------------------------------------------
-- 2. customer_portal_sessions
-- ---------------------------------------------------------------------------
CREATE TABLE customer_portal_sessions (
    id          TEXT PRIMARY KEY DEFAULT 'vlx_cps_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id   TEXT NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
    livemode    BOOLEAN NOT NULL DEFAULT true,
    customer_id TEXT NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    -- sha256(token) — the raw token is returned once at create time and never
    -- stored, matching the api_keys / payment_update_tokens pattern.
    token_hash  TEXT NOT NULL UNIQUE,
    expires_at  TIMESTAMPTZ NOT NULL,
    revoked_at  TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_customer_portal_sessions_customer
    ON customer_portal_sessions (tenant_id, livemode, customer_id, created_at DESC);

CREATE INDEX idx_customer_portal_sessions_expires
    ON customer_portal_sessions (expires_at)
    WHERE revoked_at IS NULL;

ALTER TABLE customer_portal_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE customer_portal_sessions FORCE  ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON customer_portal_sessions FOR ALL USING (
    current_setting('app.bypass_rls', true) = 'on'
    OR (
        tenant_id = current_setting('app.tenant_id', true)
        AND livemode = (current_setting('app.livemode', true) IS DISTINCT FROM 'off')
    )
);

GRANT ALL ON TABLE customer_portal_sessions TO velox_app;

CREATE TRIGGER set_livemode
    BEFORE INSERT ON customer_portal_sessions
    FOR EACH ROW EXECUTE FUNCTION set_livemode_from_session();
