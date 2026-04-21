-- Per-tenant Stripe credentials.
--
-- Velox moves Stripe credentials off the operator's environment and onto each
-- tenant. Every tenant connects their own Stripe account for each mode (test
-- + live), and Velox orchestrates billing through those credentials rather
-- than a shared platform-level Stripe account. This mirrors Chargebee /
-- Recurly / Stripe-Billing and is the right model for a multi-tenant billing
-- engine — the alternative (platform Stripe) only makes sense for merchant-
-- of-record products (Paddle, Lemon Squeezy).
--
-- Why a separate row per (tenant, livemode):
--   * Test credentials need strict isolation from live — the whole point of
--     test mode is that a compromise of test keys can't spend real money.
--     Row-level separation makes RLS policies trivial (already mode-aware
--     via app.livemode since 0020).
--   * Tenants frequently connect test before live (or live before test).
--     UNIQUE(tenant_id, livemode) lets them do one without the other.
--
-- Secret handling mirrors 0019 (webhook_endpoints.secret_encrypted): the raw
-- Stripe secret key and webhook signing secret never land on disk in
-- plaintext. secret_key_last4 / webhook_secret_last4 let the UI identify
-- which key is connected without re-exposing the plaintext after connect.
CREATE TABLE stripe_provider_credentials (
    id                        TEXT PRIMARY KEY DEFAULT 'vlx_spc_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id                 TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    livemode                  BOOLEAN NOT NULL,

    -- Stripe account identity (set on successful verify; display-only).
    stripe_account_id         TEXT,
    stripe_account_name       TEXT,

    -- Credentials at rest. enc: envelope from internal/platform/crypto.
    secret_key_encrypted      TEXT NOT NULL,
    secret_key_last4          TEXT NOT NULL,
    publishable_key           TEXT NOT NULL,

    -- Webhook signing secret. Nullable: a tenant can connect API creds
    -- first and register the webhook endpoint in a second step.
    webhook_secret_encrypted  TEXT,
    webhook_secret_last4      TEXT,

    -- Health / audit.
    verified_at               TIMESTAMPTZ,
    last_verified_error       TEXT,
    created_at                TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at                TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE (tenant_id, livemode)
);

CREATE INDEX idx_stripe_provider_credentials_tenant ON stripe_provider_credentials (tenant_id);

ALTER TABLE stripe_provider_credentials ENABLE ROW LEVEL SECURITY;

-- Mode-aware tenant isolation (matches the 0020 pattern).
CREATE POLICY tenant_isolation ON stripe_provider_credentials FOR ALL USING (
    current_setting('app.bypass_rls', true) = 'on'
    OR (
        tenant_id = current_setting('app.tenant_id', true)
        AND livemode = (current_setting('app.livemode', true) IS DISTINCT FROM 'off')
    )
);

GRANT ALL ON TABLE stripe_provider_credentials TO velox_app;
