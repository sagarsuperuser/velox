-- FEAT-8: Stripe-style test mode.
--
-- Adds livemode BOOLEAN to every mode-aware table, extends the
-- tenant_isolation RLS predicate to filter by app.livemode session var,
-- introduces test_clocks for frozen-time simulation of subscription
-- lifecycles, and wires subscriptions.test_clock_id.
--
-- Mode-neutral tables (tenants, users, tenant_settings, feature_flags,
-- feature_flag_overrides) are NOT touched — account-level configuration is
-- shared across modes, mirroring Stripe's account settings model.
--
-- Existing rows default to livemode=true (live) — this preserves production
-- behavior for all pre-migration data. New test-mode data is inserted with
-- livemode=false by explicit callers.

-- ---------------------------------------------------------------------------
-- 1. Add livemode column to mode-aware tables.
-- ---------------------------------------------------------------------------
ALTER TABLE api_keys                     ADD COLUMN livemode BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE audit_log                    ADD COLUMN livemode BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE billed_entries               ADD COLUMN livemode BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE billing_provider_connections ADD COLUMN livemode BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE coupon_redemptions           ADD COLUMN livemode BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE coupons                      ADD COLUMN livemode BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE credit_note_line_items       ADD COLUMN livemode BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE credit_notes                 ADD COLUMN livemode BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE customer_billing_profiles    ADD COLUMN livemode BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE customer_credit_ledger       ADD COLUMN livemode BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE customer_dunning_overrides   ADD COLUMN livemode BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE customer_payment_setups      ADD COLUMN livemode BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE customer_price_overrides     ADD COLUMN livemode BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE customers                    ADD COLUMN livemode BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE dunning_policies             ADD COLUMN livemode BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE email_outbox                 ADD COLUMN livemode BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE idempotency_keys             ADD COLUMN livemode BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE invoice_dunning_events       ADD COLUMN livemode BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE invoice_dunning_runs         ADD COLUMN livemode BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE invoice_line_items           ADD COLUMN livemode BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE invoices                     ADD COLUMN livemode BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE meters                       ADD COLUMN livemode BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE payment_update_tokens        ADD COLUMN livemode BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE plans                        ADD COLUMN livemode BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE rating_rule_versions         ADD COLUMN livemode BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE stripe_webhook_events        ADD COLUMN livemode BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE subscriptions                ADD COLUMN livemode BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE usage_events                 ADD COLUMN livemode BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE webhook_deliveries           ADD COLUMN livemode BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE webhook_endpoints            ADD COLUMN livemode BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE webhook_events               ADD COLUMN livemode BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE webhook_outbox               ADD COLUMN livemode BOOLEAN NOT NULL DEFAULT true;

-- ---------------------------------------------------------------------------
-- 2. Widen UNIQUE constraints that guard user-supplied keys so the same key
--    can exist once per mode (e.g. plan code "pro" in both test and live).
--    FK-scoped indexes (subscription_id, customer_id, ...) inherit mode from
--    the referenced row's PK and need no change.
-- ---------------------------------------------------------------------------
ALTER TABLE customers             DROP CONSTRAINT customers_tenant_id_external_id_key;
ALTER TABLE customers             ADD  UNIQUE (tenant_id, livemode, external_id);

ALTER TABLE rating_rule_versions  DROP CONSTRAINT rating_rule_versions_tenant_id_rule_key_version_key;
ALTER TABLE rating_rule_versions  ADD  UNIQUE (tenant_id, livemode, rule_key, version);

ALTER TABLE meters                DROP CONSTRAINT meters_tenant_id_key_key;
ALTER TABLE meters                ADD  UNIQUE (tenant_id, livemode, key);

ALTER TABLE plans                 DROP CONSTRAINT plans_tenant_id_code_key;
ALTER TABLE plans                 ADD  UNIQUE (tenant_id, livemode, code);

ALTER TABLE subscriptions         DROP CONSTRAINT subscriptions_tenant_id_code_key;
ALTER TABLE subscriptions         ADD  UNIQUE (tenant_id, livemode, code);

ALTER TABLE usage_events          DROP CONSTRAINT usage_events_tenant_id_idempotency_key_key;
ALTER TABLE usage_events          ADD  UNIQUE (tenant_id, livemode, idempotency_key);

ALTER TABLE billed_entries        DROP CONSTRAINT billed_entries_tenant_id_idempotency_key_key;
ALTER TABLE billed_entries        ADD  UNIQUE (tenant_id, livemode, idempotency_key);

ALTER TABLE invoices              DROP CONSTRAINT invoices_tenant_id_invoice_number_key;
ALTER TABLE invoices              ADD  UNIQUE (tenant_id, livemode, invoice_number);

ALTER TABLE credit_notes          DROP CONSTRAINT credit_notes_tenant_id_credit_note_number_key;
ALTER TABLE credit_notes          ADD  UNIQUE (tenant_id, livemode, credit_note_number);

ALTER TABLE coupons               DROP CONSTRAINT coupons_tenant_id_code_key;
ALTER TABLE coupons               ADD  UNIQUE (tenant_id, livemode, code);

ALTER TABLE dunning_policies      DROP CONSTRAINT dunning_policies_tenant_id_key;
ALTER TABLE dunning_policies      ADD  UNIQUE (tenant_id, livemode);

ALTER TABLE stripe_webhook_events DROP CONSTRAINT stripe_webhook_events_tenant_id_stripe_event_id_key;
ALTER TABLE stripe_webhook_events ADD  UNIQUE (tenant_id, livemode, stripe_event_id);

-- idempotency_keys: PRIMARY KEY widens to include livemode.
ALTER TABLE idempotency_keys DROP CONSTRAINT idempotency_keys_pkey;
ALTER TABLE idempotency_keys ADD PRIMARY KEY (tenant_id, livemode, key);

-- ---------------------------------------------------------------------------
-- 3. Extend tenant_isolation RLS policies with livemode predicate.
--
--    New semantics: row is visible iff
--      bypass is on, OR
--      tenant matches AND livemode matches app.livemode session var.
--
--    app.livemode encoding: 'off' → test mode; anything else (including unset
--    or 'on') → live mode. Defaulting unset→live is fault-tolerant: any caller
--    that forgets to propagate mode falls back to production behavior, not an
--    empty result set.
--
--    tenant_settings keeps the mode-neutral policy from 0006 — account-level
--    configuration is shared across modes (branding, defaults, preferences).
-- ---------------------------------------------------------------------------
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
            'dunning_policies', 'email_outbox', 'idempotency_keys', 'invoice_dunning_events',
            'invoice_dunning_runs', 'invoice_line_items', 'invoices', 'meters',
            'payment_update_tokens', 'plans', 'rating_rule_versions',
            'stripe_webhook_events', 'subscriptions', 'usage_events',
            'webhook_deliveries', 'webhook_endpoints', 'webhook_events',
            'webhook_outbox'
        ])
    LOOP
        EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON %I', tbl);
        EXECUTE format(
            'CREATE POLICY tenant_isolation ON %I FOR ALL USING (
                current_setting(''app.bypass_rls'', true) = ''on''
                OR (
                    tenant_id = current_setting(''app.tenant_id'', true)
                    AND livemode = (current_setting(''app.livemode'', true) IS DISTINCT FROM ''off'')
                )
            )',
            tbl
        );
    END LOOP;
END $$;

-- ---------------------------------------------------------------------------
-- 4. test_clocks: frozen-time simulator for test-mode subscription lifecycles.
--
--    Mirrors Stripe's TestClock API — each clock holds a frozen_time that the
--    billing engine reads instead of wall-clock when processing a subscription
--    attached to the clock. Advancing the clock triggers cycle boundaries,
--    trial ends, dunning retries etc. in compressed wall-clock time.
--
--    CHECK (livemode = false) enforces "test clocks exist only in test mode."
-- ---------------------------------------------------------------------------
CREATE TABLE test_clocks (
    id             TEXT PRIMARY KEY DEFAULT 'vlx_tclk_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id      TEXT NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
    livemode       BOOLEAN NOT NULL DEFAULT false CHECK (livemode = false),
    name           TEXT NOT NULL DEFAULT '',
    frozen_time    TIMESTAMPTZ NOT NULL,
    status         TEXT NOT NULL DEFAULT 'ready'
        CHECK (status IN ('ready', 'advancing', 'internal_failure')),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    deletes_after  TIMESTAMPTZ
);

CREATE INDEX idx_test_clocks_tenant  ON test_clocks (tenant_id, created_at DESC);
CREATE INDEX idx_test_clocks_deletes ON test_clocks (deletes_after) WHERE deletes_after IS NOT NULL;

ALTER TABLE test_clocks ENABLE ROW LEVEL SECURITY;
ALTER TABLE test_clocks FORCE  ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON test_clocks FOR ALL USING (
    current_setting('app.bypass_rls', true) = 'on'
    OR (
        tenant_id = current_setting('app.tenant_id', true)
        AND livemode = (current_setting('app.livemode', true) IS DISTINCT FROM 'off')
    )
);

GRANT ALL ON TABLE test_clocks TO velox_app;

-- ---------------------------------------------------------------------------
-- 5. Attach subscriptions to test clocks.
--
--    CHECK constraint enforces that test_clock_id can only be set on
--    test-mode subscriptions — a live-mode sub referencing a test clock would
--    corrupt real customer billing timelines.
-- ---------------------------------------------------------------------------
ALTER TABLE subscriptions
    ADD COLUMN test_clock_id TEXT REFERENCES test_clocks(id) ON DELETE SET NULL;

ALTER TABLE subscriptions
    ADD CONSTRAINT subscriptions_test_clock_requires_testmode
    CHECK (test_clock_id IS NULL OR livemode = false);

CREATE INDEX idx_subscriptions_test_clock
    ON subscriptions (test_clock_id)
    WHERE test_clock_id IS NOT NULL;
