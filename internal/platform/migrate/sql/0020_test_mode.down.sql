-- Roll back FEAT-8 test mode.
--
-- NOTE: this down migration will FAIL if any test-mode rows (livemode=false)
-- exist and their non-livemode key tuple collides with a live-mode row — the
-- UNIQUE constraints restored here can't accept the merged set. Operator must
-- DELETE test-mode rows first if that happens. Down migrations are rare and
-- typically run in dev; we prefer loud failure over silent destruction.

-- 1. Subscriptions: detach from test_clocks.
DROP INDEX IF EXISTS idx_subscriptions_test_clock;
ALTER TABLE subscriptions DROP CONSTRAINT IF EXISTS subscriptions_test_clock_requires_testmode;
ALTER TABLE subscriptions DROP COLUMN IF EXISTS test_clock_id;

-- 2. test_clocks.
DROP TABLE IF EXISTS test_clocks;

-- 3. Restore original tenant_isolation policies (no livemode predicate).
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
                OR tenant_id = current_setting(''app.tenant_id'', true)
            )',
            tbl
        );
    END LOOP;
END $$;

-- 4. Restore original UNIQUE constraints.
ALTER TABLE idempotency_keys DROP CONSTRAINT idempotency_keys_pkey;
ALTER TABLE idempotency_keys ADD PRIMARY KEY (tenant_id, key);

ALTER TABLE stripe_webhook_events DROP CONSTRAINT stripe_webhook_events_tenant_id_livemode_stripe_event_id_key;
ALTER TABLE stripe_webhook_events ADD  UNIQUE (tenant_id, stripe_event_id);

ALTER TABLE dunning_policies DROP CONSTRAINT dunning_policies_tenant_id_livemode_key;
ALTER TABLE dunning_policies ADD  UNIQUE (tenant_id);

ALTER TABLE coupons DROP CONSTRAINT coupons_tenant_id_livemode_code_key;
ALTER TABLE coupons ADD  UNIQUE (tenant_id, code);

ALTER TABLE credit_notes DROP CONSTRAINT credit_notes_tenant_id_livemode_credit_note_number_key;
ALTER TABLE credit_notes ADD  UNIQUE (tenant_id, credit_note_number);

ALTER TABLE invoices DROP CONSTRAINT invoices_tenant_id_livemode_invoice_number_key;
ALTER TABLE invoices ADD  UNIQUE (tenant_id, invoice_number);

ALTER TABLE billed_entries DROP CONSTRAINT billed_entries_tenant_id_livemode_idempotency_key_key;
ALTER TABLE billed_entries ADD  UNIQUE (tenant_id, idempotency_key);

ALTER TABLE usage_events DROP CONSTRAINT usage_events_tenant_id_livemode_idempotency_key_key;
ALTER TABLE usage_events ADD  UNIQUE (tenant_id, idempotency_key);

ALTER TABLE subscriptions DROP CONSTRAINT subscriptions_tenant_id_livemode_code_key;
ALTER TABLE subscriptions ADD  UNIQUE (tenant_id, code);

ALTER TABLE plans DROP CONSTRAINT plans_tenant_id_livemode_code_key;
ALTER TABLE plans ADD  UNIQUE (tenant_id, code);

ALTER TABLE meters DROP CONSTRAINT meters_tenant_id_livemode_key_key;
ALTER TABLE meters ADD  UNIQUE (tenant_id, key);

ALTER TABLE rating_rule_versions DROP CONSTRAINT rating_rule_versions_tenant_id_livemode_rule_key_version_key;
ALTER TABLE rating_rule_versions ADD  UNIQUE (tenant_id, rule_key, version);

ALTER TABLE customers DROP CONSTRAINT customers_tenant_id_livemode_external_id_key;
ALTER TABLE customers ADD  UNIQUE (tenant_id, external_id);

-- 5. Drop livemode columns.
ALTER TABLE webhook_outbox               DROP COLUMN livemode;
ALTER TABLE webhook_events               DROP COLUMN livemode;
ALTER TABLE webhook_endpoints            DROP COLUMN livemode;
ALTER TABLE webhook_deliveries           DROP COLUMN livemode;
ALTER TABLE usage_events                 DROP COLUMN livemode;
ALTER TABLE subscriptions                DROP COLUMN livemode;
ALTER TABLE stripe_webhook_events        DROP COLUMN livemode;
ALTER TABLE rating_rule_versions         DROP COLUMN livemode;
ALTER TABLE plans                        DROP COLUMN livemode;
ALTER TABLE payment_update_tokens        DROP COLUMN livemode;
ALTER TABLE meters                       DROP COLUMN livemode;
ALTER TABLE invoices                     DROP COLUMN livemode;
ALTER TABLE invoice_line_items           DROP COLUMN livemode;
ALTER TABLE invoice_dunning_runs         DROP COLUMN livemode;
ALTER TABLE invoice_dunning_events       DROP COLUMN livemode;
ALTER TABLE idempotency_keys             DROP COLUMN livemode;
ALTER TABLE email_outbox                 DROP COLUMN livemode;
ALTER TABLE dunning_policies             DROP COLUMN livemode;
ALTER TABLE customers                    DROP COLUMN livemode;
ALTER TABLE customer_price_overrides     DROP COLUMN livemode;
ALTER TABLE customer_payment_setups      DROP COLUMN livemode;
ALTER TABLE customer_dunning_overrides   DROP COLUMN livemode;
ALTER TABLE customer_credit_ledger       DROP COLUMN livemode;
ALTER TABLE customer_billing_profiles    DROP COLUMN livemode;
ALTER TABLE credit_notes                 DROP COLUMN livemode;
ALTER TABLE credit_note_line_items       DROP COLUMN livemode;
ALTER TABLE coupons                      DROP COLUMN livemode;
ALTER TABLE coupon_redemptions           DROP COLUMN livemode;
ALTER TABLE billing_provider_connections DROP COLUMN livemode;
ALTER TABLE billed_entries               DROP COLUMN livemode;
ALTER TABLE audit_log                    DROP COLUMN livemode;
ALTER TABLE api_keys                     DROP COLUMN livemode;
