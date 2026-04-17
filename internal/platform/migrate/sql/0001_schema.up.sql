-- Velox Billing Engine — Consolidated Schema
-- Source of truth: pg_dump of production schema (21 migrations merged)

-- Extensions
CREATE EXTENSION IF NOT EXISTS pgcrypto;

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

CREATE TABLE tenant_settings (
    tenant_id           TEXT PRIMARY KEY REFERENCES tenants(id),
    default_currency    TEXT NOT NULL DEFAULT 'USD',
    timezone            TEXT NOT NULL DEFAULT 'UTC',
    invoice_prefix      TEXT NOT NULL DEFAULT 'VLX',
    invoice_next_seq    INT NOT NULL DEFAULT 1,
    net_payment_terms   INT NOT NULL DEFAULT 30,
    company_name        TEXT,
    company_address     TEXT,
    company_email       TEXT,
    company_phone       TEXT,
    logo_url            TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    credit_note_prefix  TEXT NOT NULL DEFAULT 'CN',
    credit_note_next_seq INT NOT NULL DEFAULT 1,
    tax_rate            NUMERIC(6,2) NOT NULL DEFAULT 0,
    tax_name            TEXT NOT NULL DEFAULT '',
    tax_rate_bp         INT NOT NULL DEFAULT 0
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
    last_used_at    TIMESTAMPTZ,
    key_salt        TEXT NOT NULL DEFAULT ''
);

CREATE INDEX idx_api_keys_prefix ON api_keys (key_prefix) WHERE revoked_at IS NULL;

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
    customer_id         TEXT NOT NULL REFERENCES customers(id),
    tenant_id           TEXT NOT NULL REFERENCES tenants(id),
    legal_name          TEXT,
    email               TEXT,
    phone               TEXT,
    address_line1       TEXT,
    address_line2       TEXT,
    city                TEXT,
    state               TEXT,
    postal_code         TEXT,
    country             TEXT,
    currency            TEXT DEFAULT 'USD',
    tax_identifier      TEXT,
    profile_status      TEXT NOT NULL DEFAULT 'missing' CHECK (profile_status IN ('missing', 'incomplete', 'ready')),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    tax_exempt          BOOLEAN NOT NULL DEFAULT false,
    tax_id              TEXT NOT NULL DEFAULT '',
    tax_id_type         TEXT NOT NULL DEFAULT '',
    tax_country         TEXT NOT NULL DEFAULT '',
    tax_state           TEXT NOT NULL DEFAULT '',
    tax_override_rate   NUMERIC(6,2),
    tax_override_name   TEXT NOT NULL DEFAULT '',
    tax_override_rate_bp INTEGER,
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
    card_brand                      TEXT,
    card_last4                      TEXT,
    card_exp_month                  INT,
    card_exp_year                   INT,
    PRIMARY KEY (tenant_id, customer_id)
);

CREATE TABLE customer_dunning_overrides (
    customer_id         TEXT NOT NULL REFERENCES customers(id),
    tenant_id           TEXT NOT NULL REFERENCES tenants(id),
    max_retry_attempts  INT,
    grace_period_days   INT,
    final_action        TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, customer_id)
);

-- ---------------------------------------------------------------------------
-- Pricing: Rating Rules, Meters, Plans
-- ---------------------------------------------------------------------------
CREATE TABLE rating_rule_versions (
    id                          TEXT PRIMARY KEY DEFAULT 'vlx_rrv_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id                   TEXT NOT NULL REFERENCES tenants(id),
    rule_key                    TEXT NOT NULL,
    name                        TEXT NOT NULL,
    version                     INT NOT NULL DEFAULT 1,
    lifecycle_state             TEXT NOT NULL DEFAULT 'draft' CHECK (lifecycle_state IN ('draft', 'active', 'archived')),
    mode                        TEXT NOT NULL CHECK (mode IN ('flat', 'graduated', 'package')),
    currency                    TEXT NOT NULL DEFAULT 'USD',
    flat_amount_cents           BIGINT NOT NULL DEFAULT 0,
    graduated_tiers             JSONB NOT NULL DEFAULT '[]',
    package_size                BIGINT NOT NULL DEFAULT 0,
    package_amount_cents        BIGINT NOT NULL DEFAULT 0,
    overage_unit_amount_cents   BIGINT NOT NULL DEFAULT 0,
    created_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
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

CREATE TABLE customer_price_overrides (
    id                          TEXT PRIMARY KEY DEFAULT 'vlx_cpo_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id                   TEXT NOT NULL REFERENCES tenants(id),
    customer_id                 TEXT NOT NULL REFERENCES customers(id),
    rating_rule_version_id      TEXT NOT NULL REFERENCES rating_rule_versions(id),
    mode                        TEXT NOT NULL CHECK (mode IN ('flat', 'graduated', 'package')),
    flat_amount_cents           BIGINT NOT NULL DEFAULT 0,
    graduated_tiers             JSONB NOT NULL DEFAULT '[]',
    package_size                BIGINT NOT NULL DEFAULT 0,
    package_amount_cents        BIGINT NOT NULL DEFAULT 0,
    overage_unit_amount_cents   BIGINT NOT NULL DEFAULT 0,
    reason                      TEXT,
    active                      BOOLEAN NOT NULL DEFAULT true,
    created_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, customer_id, rating_rule_version_id)
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
    previous_plan_id                TEXT,
    plan_changed_at                 TIMESTAMPTZ,
    usage_cap_units                 BIGINT,
    overage_action                  TEXT NOT NULL DEFAULT 'charge',
    UNIQUE (tenant_id, code)
);

CREATE INDEX idx_subscriptions_next_billing
    ON subscriptions (next_billing_at, status)
    WHERE status = 'active';

-- ---------------------------------------------------------------------------
-- Usage Events & Billed Entries
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

CREATE INDEX idx_usage_events_customer_meter
    ON usage_events (tenant_id, customer_id, meter_id, timestamp);

CREATE INDEX idx_usage_events_aggregate
    ON usage_events (tenant_id, customer_id, meter_id, timestamp);

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
    credits_applied_cents       BIGINT NOT NULL DEFAULT 0,
    tax_rate                    NUMERIC(6,2) NOT NULL DEFAULT 0,
    tax_name                    TEXT NOT NULL DEFAULT '',
    tax_country                 TEXT NOT NULL DEFAULT '',
    tax_id                      TEXT NOT NULL DEFAULT '',
    tax_rate_bp                 INT NOT NULL DEFAULT 0,
    auto_charge_pending         BOOLEAN NOT NULL DEFAULT false,
    UNIQUE (tenant_id, invoice_number)
);

-- Invoice performance indexes
CREATE INDEX idx_invoices_tenant_customer ON invoices (tenant_id, customer_id);
CREATE INDEX idx_invoices_tenant_status ON invoices (tenant_id, status);
CREATE INDEX idx_invoices_tenant_payment_status ON invoices (tenant_id, payment_status);
CREATE INDEX idx_invoices_tenant_due_at ON invoices (tenant_id, due_at) WHERE due_at IS NOT NULL;
CREATE INDEX idx_invoices_tenant_subscription ON invoices (tenant_id, subscription_id);
CREATE INDEX idx_invoices_tenant_created ON invoices (tenant_id, created_at DESC);

-- Invoice idempotency: prevent duplicate invoices per billing period
CREATE UNIQUE INDEX idx_invoices_billing_idempotency
    ON invoices (tenant_id, subscription_id, billing_period_start, billing_period_end)
    WHERE status != 'voided';

-- Auto-charge tracking for reliable payment retry
CREATE INDEX idx_invoices_auto_charge
    ON invoices (auto_charge_pending)
    WHERE auto_charge_pending = TRUE AND payment_status = 'pending';

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
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    proration_factor        NUMERIC(10,6),
    tax_rate_bp             INT NOT NULL DEFAULT 0
);

CREATE INDEX idx_line_items_invoice ON invoice_line_items (invoice_id);

-- ---------------------------------------------------------------------------
-- Credit Notes
-- ---------------------------------------------------------------------------
CREATE TABLE credit_notes (
    id                      TEXT PRIMARY KEY DEFAULT 'vlx_cn_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id               TEXT NOT NULL REFERENCES tenants(id),
    invoice_id              TEXT NOT NULL REFERENCES invoices(id),
    customer_id             TEXT NOT NULL REFERENCES customers(id),
    credit_note_number      TEXT NOT NULL,
    status                  TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'issued', 'voided')),
    reason                  TEXT NOT NULL,
    subtotal_cents          BIGINT NOT NULL DEFAULT 0,
    tax_amount_cents        BIGINT NOT NULL DEFAULT 0,
    total_cents             BIGINT NOT NULL DEFAULT 0,
    refund_amount_cents     BIGINT NOT NULL DEFAULT 0,
    credit_amount_cents     BIGINT NOT NULL DEFAULT 0,
    currency                TEXT NOT NULL DEFAULT 'USD',
    issued_at               TIMESTAMPTZ,
    voided_at               TIMESTAMPTZ,
    refund_status           TEXT DEFAULT 'none' CHECK (refund_status IN ('none', 'pending', 'succeeded', 'failed')),
    stripe_refund_id        TEXT,
    metadata                JSONB NOT NULL DEFAULT '{}',
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, credit_note_number)
);

CREATE TABLE credit_note_line_items (
    id                      TEXT PRIMARY KEY DEFAULT 'vlx_cnli_' || encode(gen_random_bytes(12), 'hex'),
    credit_note_id          TEXT NOT NULL REFERENCES credit_notes(id),
    tenant_id               TEXT NOT NULL REFERENCES tenants(id),
    invoice_line_item_id    TEXT,
    description             TEXT NOT NULL,
    quantity                BIGINT NOT NULL DEFAULT 0,
    unit_amount_cents       BIGINT NOT NULL DEFAULT 0,
    amount_cents            BIGINT NOT NULL DEFAULT 0,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------------------
-- Customer Credit Ledger (event-sourced)
-- ---------------------------------------------------------------------------
CREATE TABLE customer_credit_ledger (
    id              TEXT PRIMARY KEY DEFAULT 'vlx_ccl_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id       TEXT NOT NULL REFERENCES tenants(id),
    customer_id     TEXT NOT NULL REFERENCES customers(id),
    entry_type      TEXT NOT NULL CHECK (entry_type IN ('grant', 'usage', 'expiry', 'adjustment')),
    amount_cents    BIGINT NOT NULL,
    balance_after   BIGINT NOT NULL,
    description     TEXT NOT NULL,
    invoice_id      TEXT,
    expires_at      TIMESTAMPTZ,
    metadata        JSONB NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_credit_ledger_customer
    ON customer_credit_ledger (tenant_id, customer_id, created_at);

-- ---------------------------------------------------------------------------
-- Coupons
-- ---------------------------------------------------------------------------
CREATE TABLE coupons (
    id                  TEXT PRIMARY KEY,
    tenant_id           TEXT NOT NULL REFERENCES tenants(id),
    code                TEXT NOT NULL,
    name                TEXT NOT NULL DEFAULT '',
    type                TEXT NOT NULL DEFAULT 'percentage',
    amount_off          BIGINT NOT NULL DEFAULT 0,
    percent_off         NUMERIC(5,2) NOT NULL DEFAULT 0,
    currency            TEXT NOT NULL DEFAULT '',
    max_redemptions     INT,
    times_redeemed      INT NOT NULL DEFAULT 0,
    expires_at          TIMESTAMPTZ,
    active              BOOLEAN NOT NULL DEFAULT true,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    plan_ids            TEXT[] NOT NULL DEFAULT '{}',
    percent_off_bp      INT NOT NULL DEFAULT 0,
    UNIQUE (tenant_id, code)
);

CREATE INDEX idx_coupons_tenant_active ON coupons (tenant_id, active);

CREATE TABLE coupon_redemptions (
    id              TEXT PRIMARY KEY,
    tenant_id       TEXT NOT NULL REFERENCES tenants(id),
    coupon_id       TEXT NOT NULL REFERENCES coupons(id),
    customer_id     TEXT NOT NULL,
    subscription_id TEXT,
    invoice_id      TEXT,
    discount_cents  BIGINT NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_coupon_redemptions_coupon ON coupon_redemptions (tenant_id, coupon_id, created_at);
CREATE INDEX idx_coupon_redemptions_customer ON coupon_redemptions (tenant_id, customer_id);

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

CREATE INDEX idx_dunning_runs_due
    ON invoice_dunning_runs (tenant_id, state, next_action_at)
    WHERE state IN ('active', 'escalated');

CREATE INDEX idx_dunning_runs_invoice
    ON invoice_dunning_runs (tenant_id, invoice_id);

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
-- Outbound Webhooks
-- ---------------------------------------------------------------------------
CREATE TABLE webhook_endpoints (
    id              TEXT PRIMARY KEY DEFAULT 'vlx_whe_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id       TEXT NOT NULL REFERENCES tenants(id),
    url             TEXT NOT NULL,
    description     TEXT,
    secret          TEXT NOT NULL,
    events          JSONB NOT NULL DEFAULT '["*"]',
    active          BOOLEAN NOT NULL DEFAULT true,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE webhook_events (
    id              TEXT PRIMARY KEY DEFAULT 'vlx_whevt_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id       TEXT NOT NULL REFERENCES tenants(id),
    event_type      TEXT NOT NULL,
    payload         JSONB NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_webhook_events_tenant ON webhook_events (tenant_id, created_at DESC);

CREATE TABLE webhook_deliveries (
    id                  TEXT PRIMARY KEY DEFAULT 'vlx_whd_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id           TEXT NOT NULL REFERENCES tenants(id),
    webhook_endpoint_id TEXT NOT NULL REFERENCES webhook_endpoints(id),
    webhook_event_id    TEXT NOT NULL REFERENCES webhook_events(id),
    status              TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'succeeded', 'failed')),
    http_status_code    INT,
    response_body       TEXT,
    error_message       TEXT,
    attempt_count       INT NOT NULL DEFAULT 0,
    next_retry_at       TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at        TIMESTAMPTZ
);

CREATE INDEX idx_webhook_deliveries_pending
    ON webhook_deliveries (status, next_retry_at)
    WHERE status = 'pending';

-- ---------------------------------------------------------------------------
-- Stripe Webhook Events
-- ---------------------------------------------------------------------------
CREATE TABLE stripe_webhook_events (
    id                      TEXT PRIMARY KEY DEFAULT 'vlx_swe_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id               TEXT NOT NULL REFERENCES tenants(id),
    stripe_event_id         TEXT NOT NULL,
    event_type              TEXT NOT NULL,
    object_type             TEXT NOT NULL,
    invoice_id              TEXT,
    customer_external_id    TEXT,
    payment_intent_id       TEXT,
    payment_status          TEXT,
    amount_cents            BIGINT,
    currency                TEXT,
    failure_message         TEXT,
    payload                 JSONB NOT NULL DEFAULT '{}',
    received_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    occurred_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, stripe_event_id)
);

-- ---------------------------------------------------------------------------
-- Payment Update Tokens
-- ---------------------------------------------------------------------------
CREATE TABLE payment_update_tokens (
    id              TEXT PRIMARY KEY,
    tenant_id       TEXT NOT NULL REFERENCES tenants(id),
    customer_id     TEXT NOT NULL REFERENCES customers(id),
    invoice_id      TEXT NOT NULL REFERENCES invoices(id),
    token_hash      TEXT NOT NULL,
    expires_at      TIMESTAMPTZ NOT NULL,
    used_at         TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_payment_tokens_expires ON payment_update_tokens (expires_at);
CREATE INDEX idx_payment_tokens_hash ON payment_update_tokens (token_hash) WHERE used_at IS NULL;

-- ---------------------------------------------------------------------------
-- Idempotency Keys
-- ---------------------------------------------------------------------------
CREATE TABLE idempotency_keys (
    tenant_id       TEXT NOT NULL,
    key             TEXT NOT NULL,
    http_method     TEXT NOT NULL,
    http_path       TEXT NOT NULL,
    status_code     INT NOT NULL,
    response_body   BYTEA NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at      TIMESTAMPTZ NOT NULL DEFAULT (now() + INTERVAL '24 hours'),
    PRIMARY KEY (tenant_id, key)
);

CREATE INDEX idx_idempotency_expires ON idempotency_keys (expires_at);

-- ---------------------------------------------------------------------------
-- Audit Log
-- ---------------------------------------------------------------------------
CREATE TABLE audit_log (
    id              TEXT PRIMARY KEY DEFAULT 'vlx_aud_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id       TEXT NOT NULL REFERENCES tenants(id),
    actor_type      TEXT NOT NULL CHECK (actor_type IN ('api_key', 'user', 'system')),
    actor_id        TEXT NOT NULL,
    action          TEXT NOT NULL,
    resource_type   TEXT NOT NULL,
    resource_id     TEXT NOT NULL,
    metadata        JSONB NOT NULL DEFAULT '{}',
    ip_address      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    resource_label  TEXT DEFAULT ''
);

CREATE INDEX idx_audit_log_resource ON audit_log (tenant_id, resource_type, resource_id);
CREATE INDEX idx_audit_log_created ON audit_log (tenant_id, created_at DESC);
CREATE INDEX idx_audit_log_tenant ON audit_log (tenant_id, created_at DESC);

-- ---------------------------------------------------------------------------
-- Feature Flags
-- ---------------------------------------------------------------------------
CREATE TABLE feature_flags (
    key         TEXT PRIMARY KEY,
    enabled     BOOLEAN NOT NULL DEFAULT FALSE,
    description TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE feature_flag_overrides (
    flag_key    TEXT NOT NULL REFERENCES feature_flags(key) ON DELETE CASCADE,
    tenant_id   TEXT NOT NULL,
    enabled     BOOLEAN NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (flag_key, tenant_id)
);

-- ===========================================================================
-- Row-Level Security
-- ===========================================================================

-- Enable + Force RLS on all tenant-scoped tables
ALTER TABLE api_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE api_keys FORCE ROW LEVEL SECURITY;
ALTER TABLE audit_log ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_log FORCE ROW LEVEL SECURITY;
ALTER TABLE billed_entries ENABLE ROW LEVEL SECURITY;
ALTER TABLE billed_entries FORCE ROW LEVEL SECURITY;
ALTER TABLE billing_provider_connections ENABLE ROW LEVEL SECURITY;
ALTER TABLE billing_provider_connections FORCE ROW LEVEL SECURITY;
ALTER TABLE coupon_redemptions ENABLE ROW LEVEL SECURITY;
ALTER TABLE coupon_redemptions FORCE ROW LEVEL SECURITY;
ALTER TABLE coupons ENABLE ROW LEVEL SECURITY;
ALTER TABLE coupons FORCE ROW LEVEL SECURITY;
ALTER TABLE credit_note_line_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE credit_note_line_items FORCE ROW LEVEL SECURITY;
ALTER TABLE credit_notes ENABLE ROW LEVEL SECURITY;
ALTER TABLE credit_notes FORCE ROW LEVEL SECURITY;
ALTER TABLE customer_billing_profiles ENABLE ROW LEVEL SECURITY;
ALTER TABLE customer_billing_profiles FORCE ROW LEVEL SECURITY;
ALTER TABLE customer_credit_ledger ENABLE ROW LEVEL SECURITY;
ALTER TABLE customer_credit_ledger FORCE ROW LEVEL SECURITY;
ALTER TABLE customer_dunning_overrides ENABLE ROW LEVEL SECURITY;
ALTER TABLE customer_dunning_overrides FORCE ROW LEVEL SECURITY;
ALTER TABLE customer_payment_setups ENABLE ROW LEVEL SECURITY;
ALTER TABLE customer_payment_setups FORCE ROW LEVEL SECURITY;
ALTER TABLE customer_price_overrides ENABLE ROW LEVEL SECURITY;
ALTER TABLE customer_price_overrides FORCE ROW LEVEL SECURITY;
ALTER TABLE customers ENABLE ROW LEVEL SECURITY;
ALTER TABLE customers FORCE ROW LEVEL SECURITY;
ALTER TABLE dunning_policies ENABLE ROW LEVEL SECURITY;
ALTER TABLE dunning_policies FORCE ROW LEVEL SECURITY;
ALTER TABLE invoice_dunning_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE invoice_dunning_events FORCE ROW LEVEL SECURITY;
ALTER TABLE invoice_dunning_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE invoice_dunning_runs FORCE ROW LEVEL SECURITY;
ALTER TABLE invoice_line_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE invoice_line_items FORCE ROW LEVEL SECURITY;
ALTER TABLE invoices ENABLE ROW LEVEL SECURITY;
ALTER TABLE invoices FORCE ROW LEVEL SECURITY;
ALTER TABLE meters ENABLE ROW LEVEL SECURITY;
ALTER TABLE meters FORCE ROW LEVEL SECURITY;
ALTER TABLE plans ENABLE ROW LEVEL SECURITY;
ALTER TABLE plans FORCE ROW LEVEL SECURITY;
ALTER TABLE rating_rule_versions ENABLE ROW LEVEL SECURITY;
ALTER TABLE rating_rule_versions FORCE ROW LEVEL SECURITY;
ALTER TABLE stripe_webhook_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE stripe_webhook_events FORCE ROW LEVEL SECURITY;
ALTER TABLE subscriptions ENABLE ROW LEVEL SECURITY;
ALTER TABLE subscriptions FORCE ROW LEVEL SECURITY;
ALTER TABLE usage_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE usage_events FORCE ROW LEVEL SECURITY;
ALTER TABLE webhook_deliveries ENABLE ROW LEVEL SECURITY;
ALTER TABLE webhook_deliveries FORCE ROW LEVEL SECURITY;
ALTER TABLE webhook_endpoints ENABLE ROW LEVEL SECURITY;
ALTER TABLE webhook_endpoints FORCE ROW LEVEL SECURITY;
ALTER TABLE webhook_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE webhook_events FORCE ROW LEVEL SECURITY;

-- RLS policy: allow access when tenant_id matches session var OR bypass is on.
DO $$
DECLARE
    tbl TEXT;
BEGIN
    FOR tbl IN
        SELECT unnest(ARRAY[
            'api_keys', 'audit_log', 'billed_entries', 'billing_provider_connections',
            'coupon_redemptions', 'coupons', 'credit_note_line_items', 'credit_notes',
            'customer_billing_profiles', 'customer_credit_ledger', 'customer_dunning_overrides',
            'customer_payment_setups', 'customer_price_overrides', 'customers',
            'dunning_policies', 'invoice_dunning_events', 'invoice_dunning_runs',
            'invoice_line_items', 'invoices', 'meters', 'plans', 'rating_rule_versions',
            'stripe_webhook_events', 'subscriptions', 'usage_events',
            'webhook_deliveries', 'webhook_endpoints', 'webhook_events'
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

-- ===========================================================================
-- Grants — velox_app role (non-superuser, RLS enforced)
-- ===========================================================================
DO $$
DECLARE
    tbl TEXT;
BEGIN
    FOR tbl IN SELECT tablename FROM pg_tables WHERE schemaname = 'public'
    LOOP
        EXECUTE format('GRANT ALL ON TABLE %I TO velox_app', tbl);
    END LOOP;
END $$;
