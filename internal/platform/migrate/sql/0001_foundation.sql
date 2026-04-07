-- Velox Foundation Schema
-- Enables RLS, creates core tables, applies tenant isolation policies.

-- Extensions
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- ---------------------------------------------------------------------------
-- Tenants
-- ---------------------------------------------------------------------------
CREATE TABLE tenants (
    id              TEXT PRIMARY KEY DEFAULT 'vlx_ten_' || encode(gen_random_bytes(12), 'hex'),
    name            TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended', 'deleted')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------------------
-- Billing Provider Connections
-- ---------------------------------------------------------------------------
CREATE TABLE billing_provider_connections (
    id              TEXT PRIMARY KEY DEFAULT 'vlx_bpc_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id       TEXT NOT NULL REFERENCES tenants(id),
    provider_type   TEXT NOT NULL DEFAULT 'stripe' CHECK (provider_type IN ('stripe')),
    environment     TEXT NOT NULL DEFAULT 'production',
    display_name    TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'connected', 'sync_error', 'disabled')),
    secret_ref      TEXT,
    last_synced_at  TIMESTAMPTZ,
    last_sync_error TEXT,
    connected_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------------------
-- Customers
-- ---------------------------------------------------------------------------
CREATE TABLE customers (
    id              TEXT PRIMARY KEY DEFAULT 'vlx_cus_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id       TEXT NOT NULL REFERENCES tenants(id),
    external_id     TEXT NOT NULL,
    display_name    TEXT NOT NULL,
    email           TEXT,
    status          TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, external_id)
);

CREATE TABLE customer_billing_profiles (
    customer_id     TEXT NOT NULL REFERENCES customers(id),
    tenant_id       TEXT NOT NULL REFERENCES tenants(id),
    legal_name      TEXT,
    email           TEXT,
    phone           TEXT,
    address_line1   TEXT,
    address_line2   TEXT,
    city            TEXT,
    state           TEXT,
    postal_code     TEXT,
    country         TEXT,
    currency        TEXT DEFAULT 'USD',
    tax_identifier  TEXT,
    profile_status  TEXT NOT NULL DEFAULT 'missing' CHECK (profile_status IN ('missing', 'incomplete', 'ready')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, customer_id)
);

CREATE TABLE customer_payment_setups (
    customer_id                     TEXT NOT NULL REFERENCES customers(id),
    tenant_id                       TEXT NOT NULL REFERENCES tenants(id),
    setup_status                    TEXT NOT NULL DEFAULT 'missing' CHECK (setup_status IN ('missing', 'pending', 'ready', 'error')),
    default_payment_method_present  BOOLEAN NOT NULL DEFAULT false,
    payment_method_type             TEXT,
    stripe_customer_id              TEXT,
    stripe_payment_method_id        TEXT,
    last_verified_at                TIMESTAMPTZ,
    created_at                      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, customer_id)
);

-- ---------------------------------------------------------------------------
-- Pricing: Rating Rules, Meters, Plans
-- ---------------------------------------------------------------------------
CREATE TABLE rating_rule_versions (
    id                      TEXT PRIMARY KEY DEFAULT 'vlx_rrv_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id               TEXT NOT NULL REFERENCES tenants(id),
    rule_key                TEXT NOT NULL,
    name                    TEXT NOT NULL,
    version                 INT NOT NULL DEFAULT 1,
    lifecycle_state         TEXT NOT NULL DEFAULT 'draft' CHECK (lifecycle_state IN ('draft', 'active', 'archived')),
    mode                    TEXT NOT NULL CHECK (mode IN ('flat', 'graduated', 'package')),
    currency                TEXT NOT NULL DEFAULT 'USD',
    flat_amount_cents       BIGINT NOT NULL DEFAULT 0,
    graduated_tiers         JSONB NOT NULL DEFAULT '[]',
    package_size            BIGINT NOT NULL DEFAULT 0,
    package_amount_cents    BIGINT NOT NULL DEFAULT 0,
    overage_unit_amount_cents BIGINT NOT NULL DEFAULT 0,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, rule_key, version)
);

CREATE TABLE meters (
    id                      TEXT PRIMARY KEY DEFAULT 'vlx_mtr_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id               TEXT NOT NULL REFERENCES tenants(id),
    key                     TEXT NOT NULL,
    name                    TEXT NOT NULL,
    unit                    TEXT NOT NULL DEFAULT 'unit',
    aggregation             TEXT NOT NULL DEFAULT 'sum',
    rating_rule_version_id  TEXT REFERENCES rating_rule_versions(id),
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, key)
);

CREATE TABLE plans (
    id                  TEXT PRIMARY KEY DEFAULT 'vlx_pln_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id           TEXT NOT NULL REFERENCES tenants(id),
    code                TEXT NOT NULL,
    name                TEXT NOT NULL,
    description         TEXT,
    currency            TEXT NOT NULL DEFAULT 'USD',
    billing_interval    TEXT NOT NULL DEFAULT 'monthly' CHECK (billing_interval IN ('monthly', 'yearly')),
    status              TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'active', 'archived')),
    base_amount_cents   BIGINT NOT NULL DEFAULT 0,
    meter_ids           JSONB NOT NULL DEFAULT '[]',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, code)
);

-- ---------------------------------------------------------------------------
-- Subscriptions
-- ---------------------------------------------------------------------------
CREATE TABLE subscriptions (
    id                              TEXT PRIMARY KEY DEFAULT 'vlx_sub_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id                       TEXT NOT NULL REFERENCES tenants(id),
    code                            TEXT NOT NULL,
    display_name                    TEXT NOT NULL,
    customer_id                     TEXT NOT NULL REFERENCES customers(id),
    plan_id                         TEXT NOT NULL REFERENCES plans(id),
    status                          TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'active', 'paused', 'canceled', 'archived')),
    billing_time                    TEXT NOT NULL DEFAULT 'calendar' CHECK (billing_time IN ('calendar', 'anniversary')),
    trial_start_at                  TIMESTAMPTZ,
    trial_end_at                    TIMESTAMPTZ,
    started_at                      TIMESTAMPTZ,
    activated_at                    TIMESTAMPTZ,
    canceled_at                     TIMESTAMPTZ,
    current_billing_period_start    TIMESTAMPTZ,
    current_billing_period_end      TIMESTAMPTZ,
    next_billing_at                 TIMESTAMPTZ,
    created_at                      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, code)
);

-- ---------------------------------------------------------------------------
-- Usage Events
-- ---------------------------------------------------------------------------
CREATE TABLE usage_events (
    id              TEXT PRIMARY KEY DEFAULT 'vlx_evt_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id       TEXT NOT NULL REFERENCES tenants(id),
    customer_id     TEXT NOT NULL REFERENCES customers(id),
    meter_id        TEXT NOT NULL REFERENCES meters(id),
    subscription_id TEXT REFERENCES subscriptions(id),
    quantity        BIGINT NOT NULL DEFAULT 0,
    properties      JSONB NOT NULL DEFAULT '{}',
    idempotency_key TEXT,
    timestamp       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, idempotency_key)
);

CREATE INDEX idx_usage_events_customer_meter ON usage_events (tenant_id, customer_id, meter_id, timestamp);

CREATE TABLE billed_entries (
    id              TEXT PRIMARY KEY DEFAULT 'vlx_ble_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id       TEXT NOT NULL REFERENCES tenants(id),
    customer_id     TEXT NOT NULL REFERENCES customers(id),
    meter_id        TEXT NOT NULL REFERENCES meters(id),
    amount_cents    BIGINT NOT NULL,
    idempotency_key TEXT,
    source          TEXT NOT NULL DEFAULT 'api' CHECK (source IN ('api', 'replay_adjustment')),
    timestamp       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, idempotency_key)
);

-- ---------------------------------------------------------------------------
-- Invoices
-- ---------------------------------------------------------------------------
CREATE TABLE invoices (
    id                          TEXT PRIMARY KEY DEFAULT 'vlx_inv_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id                   TEXT NOT NULL REFERENCES tenants(id),
    customer_id                 TEXT NOT NULL REFERENCES customers(id),
    subscription_id             TEXT NOT NULL REFERENCES subscriptions(id),
    invoice_number              TEXT NOT NULL,
    status                      TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'finalized', 'paid', 'voided')),
    payment_status              TEXT NOT NULL DEFAULT 'pending' CHECK (payment_status IN ('pending', 'processing', 'succeeded', 'failed')),
    currency                    TEXT NOT NULL DEFAULT 'USD',
    subtotal_cents              BIGINT NOT NULL DEFAULT 0,
    discount_cents              BIGINT NOT NULL DEFAULT 0,
    tax_amount_cents            BIGINT NOT NULL DEFAULT 0,
    total_amount_cents          BIGINT NOT NULL DEFAULT 0,
    amount_due_cents            BIGINT NOT NULL DEFAULT 0,
    amount_paid_cents           BIGINT NOT NULL DEFAULT 0,
    billing_period_start        TIMESTAMPTZ NOT NULL,
    billing_period_end          TIMESTAMPTZ NOT NULL,
    issued_at                   TIMESTAMPTZ,
    due_at                      TIMESTAMPTZ,
    paid_at                     TIMESTAMPTZ,
    voided_at                   TIMESTAMPTZ,
    stripe_payment_intent_id    TEXT,
    last_payment_error          TEXT,
    payment_overdue             BOOLEAN NOT NULL DEFAULT false,
    pdf_object_key              TEXT,
    net_payment_term_days       INT NOT NULL DEFAULT 30,
    memo                        TEXT,
    footer                      TEXT,
    metadata                    JSONB NOT NULL DEFAULT '{}',
    created_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, invoice_number)
);

CREATE TABLE invoice_line_items (
    id                      TEXT PRIMARY KEY DEFAULT 'vlx_ili_' || encode(gen_random_bytes(12), 'hex'),
    invoice_id              TEXT NOT NULL REFERENCES invoices(id),
    tenant_id               TEXT NOT NULL REFERENCES tenants(id),
    line_type               TEXT NOT NULL CHECK (line_type IN ('base_fee', 'usage', 'add_on', 'discount', 'tax')),
    meter_id                TEXT,
    description             TEXT NOT NULL,
    quantity                BIGINT NOT NULL DEFAULT 0,
    unit_amount_cents       BIGINT NOT NULL DEFAULT 0,
    amount_cents            BIGINT NOT NULL DEFAULT 0,
    tax_rate                NUMERIC(5,4) NOT NULL DEFAULT 0,
    tax_amount_cents        BIGINT NOT NULL DEFAULT 0,
    total_amount_cents      BIGINT NOT NULL DEFAULT 0,
    currency                TEXT NOT NULL DEFAULT 'USD',
    pricing_mode            TEXT,
    rating_rule_version_id  TEXT,
    billing_period_start    TIMESTAMPTZ,
    billing_period_end      TIMESTAMPTZ,
    metadata                JSONB NOT NULL DEFAULT '{}',
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------------------
-- Dunning
-- ---------------------------------------------------------------------------
CREATE TABLE dunning_policies (
    id                  TEXT PRIMARY KEY DEFAULT 'vlx_dpol_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id           TEXT NOT NULL REFERENCES tenants(id),
    name                TEXT NOT NULL,
    enabled             BOOLEAN NOT NULL DEFAULT true,
    retry_schedule      JSONB NOT NULL DEFAULT '[]',
    max_retry_attempts  INT NOT NULL DEFAULT 3,
    final_action        TEXT NOT NULL DEFAULT 'manual_review' CHECK (final_action IN ('manual_review', 'pause', 'write_off_later')),
    grace_period_days   INT NOT NULL DEFAULT 3,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id)
);

CREATE TABLE invoice_dunning_runs (
    id              TEXT PRIMARY KEY DEFAULT 'vlx_drun_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id       TEXT NOT NULL REFERENCES tenants(id),
    invoice_id      TEXT NOT NULL REFERENCES invoices(id),
    customer_id     TEXT,
    policy_id       TEXT NOT NULL REFERENCES dunning_policies(id),
    state           TEXT NOT NULL DEFAULT 'scheduled',
    reason          TEXT,
    attempt_count   INT NOT NULL DEFAULT 0,
    last_attempt_at TIMESTAMPTZ,
    next_action_at  TIMESTAMPTZ,
    paused          BOOLEAN NOT NULL DEFAULT false,
    resolved_at     TIMESTAMPTZ,
    resolution      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE invoice_dunning_events (
    id              TEXT PRIMARY KEY DEFAULT 'vlx_devt_' || encode(gen_random_bytes(12), 'hex'),
    run_id          TEXT NOT NULL REFERENCES invoice_dunning_runs(id),
    tenant_id       TEXT NOT NULL REFERENCES tenants(id),
    invoice_id      TEXT NOT NULL REFERENCES invoices(id),
    event_type      TEXT NOT NULL,
    state           TEXT NOT NULL,
    reason          TEXT,
    attempt_count   INT NOT NULL DEFAULT 0,
    metadata        JSONB NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------------------
-- Auth: Users & API Keys
-- ---------------------------------------------------------------------------
CREATE TABLE users (
    id              TEXT PRIMARY KEY DEFAULT 'vlx_usr_' || encode(gen_random_bytes(12), 'hex'),
    email           TEXT NOT NULL UNIQUE,
    display_name    TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    platform_role   TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE api_keys (
    id              TEXT PRIMARY KEY DEFAULT 'vlx_key_' || encode(gen_random_bytes(12), 'hex'),
    key_prefix      TEXT NOT NULL,
    key_hash        TEXT NOT NULL,
    key_type        TEXT NOT NULL DEFAULT 'secret' CHECK (key_type IN ('platform', 'secret', 'publishable')),
    name            TEXT NOT NULL,
    tenant_id       TEXT NOT NULL REFERENCES tenants(id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at      TIMESTAMPTZ,
    revoked_at      TIMESTAMPTZ,
    last_used_at    TIMESTAMPTZ
);

CREATE INDEX idx_api_keys_prefix ON api_keys (key_prefix) WHERE revoked_at IS NULL;

-- ---------------------------------------------------------------------------
-- Stripe Webhook Events
-- ---------------------------------------------------------------------------
CREATE TABLE stripe_webhook_events (
    id                  TEXT PRIMARY KEY DEFAULT 'vlx_swe_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id           TEXT NOT NULL REFERENCES tenants(id),
    stripe_event_id     TEXT NOT NULL,
    event_type          TEXT NOT NULL,
    object_type         TEXT NOT NULL,
    invoice_id          TEXT,
    customer_external_id TEXT,
    payment_intent_id   TEXT,
    payment_status      TEXT,
    amount_cents        BIGINT,
    currency            TEXT,
    failure_message     TEXT,
    payload             JSONB NOT NULL DEFAULT '{}',
    received_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    occurred_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, stripe_event_id)
);

-- ---------------------------------------------------------------------------
-- Row-Level Security
-- ---------------------------------------------------------------------------
-- ENABLE + FORCE ensures RLS applies even to the table owner (the app user).
ALTER TABLE customers ENABLE ROW LEVEL SECURITY; ALTER TABLE customers FORCE ROW LEVEL SECURITY;
ALTER TABLE customer_billing_profiles ENABLE ROW LEVEL SECURITY; ALTER TABLE customer_billing_profiles FORCE ROW LEVEL SECURITY;
ALTER TABLE customer_payment_setups ENABLE ROW LEVEL SECURITY; ALTER TABLE customer_payment_setups FORCE ROW LEVEL SECURITY;
ALTER TABLE rating_rule_versions ENABLE ROW LEVEL SECURITY; ALTER TABLE rating_rule_versions FORCE ROW LEVEL SECURITY;
ALTER TABLE meters ENABLE ROW LEVEL SECURITY; ALTER TABLE meters FORCE ROW LEVEL SECURITY;
ALTER TABLE plans ENABLE ROW LEVEL SECURITY; ALTER TABLE plans FORCE ROW LEVEL SECURITY;
ALTER TABLE subscriptions ENABLE ROW LEVEL SECURITY; ALTER TABLE subscriptions FORCE ROW LEVEL SECURITY;
ALTER TABLE usage_events ENABLE ROW LEVEL SECURITY; ALTER TABLE usage_events FORCE ROW LEVEL SECURITY;
ALTER TABLE billed_entries ENABLE ROW LEVEL SECURITY; ALTER TABLE billed_entries FORCE ROW LEVEL SECURITY;
ALTER TABLE invoices ENABLE ROW LEVEL SECURITY; ALTER TABLE invoices FORCE ROW LEVEL SECURITY;
ALTER TABLE invoice_line_items ENABLE ROW LEVEL SECURITY; ALTER TABLE invoice_line_items FORCE ROW LEVEL SECURITY;
ALTER TABLE dunning_policies ENABLE ROW LEVEL SECURITY; ALTER TABLE dunning_policies FORCE ROW LEVEL SECURITY;
ALTER TABLE invoice_dunning_runs ENABLE ROW LEVEL SECURITY; ALTER TABLE invoice_dunning_runs FORCE ROW LEVEL SECURITY;
ALTER TABLE invoice_dunning_events ENABLE ROW LEVEL SECURITY; ALTER TABLE invoice_dunning_events FORCE ROW LEVEL SECURITY;
ALTER TABLE billing_provider_connections ENABLE ROW LEVEL SECURITY; ALTER TABLE billing_provider_connections FORCE ROW LEVEL SECURITY;
ALTER TABLE api_keys ENABLE ROW LEVEL SECURITY; ALTER TABLE api_keys FORCE ROW LEVEL SECURITY;
ALTER TABLE stripe_webhook_events ENABLE ROW LEVEL SECURITY; ALTER TABLE stripe_webhook_events FORCE ROW LEVEL SECURITY;

-- RLS policy: allow access when tenant_id matches session var OR bypass is on.
DO $$
DECLARE
    tbl TEXT;
BEGIN
    FOR tbl IN
        SELECT unnest(ARRAY[
            'customers', 'customer_billing_profiles', 'customer_payment_setups',
            'rating_rule_versions', 'meters', 'plans', 'subscriptions',
            'usage_events', 'billed_entries', 'invoices', 'invoice_line_items',
            'dunning_policies', 'invoice_dunning_runs', 'invoice_dunning_events',
            'billing_provider_connections', 'api_keys', 'stripe_webhook_events'
        ])
    LOOP
        EXECUTE format(
            'CREATE POLICY tenant_isolation ON %I FOR ALL USING (
                current_setting(''app.bypass_rls'', true) = ''on''
                OR tenant_id = current_setting(''app.tenant_id'', true)
            )',
            tbl
        );
    END LOOP;
END $$;

-- Grant table access to the app role (non-superuser, RLS enforced).
DO $$
DECLARE
    tbl TEXT;
BEGIN
    FOR tbl IN SELECT tablename FROM pg_tables WHERE schemaname = 'public'
    LOOP
        EXECUTE format('GRANT ALL ON TABLE %I TO velox_app', tbl);
    END LOOP;
END $$;
