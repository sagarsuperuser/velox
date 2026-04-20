-- FEAT-8 P6: auto-populate livemode from the tx session on every INSERT into
-- a mode-aware table. Without this, inserts land with livemode=DEFAULT=true
-- regardless of ctx — and an INSERT under app.livemode='off' fails RLS
-- WITH CHECK because the row's livemode (true) doesn't match the session
-- filter. Rather than patch every INSERT site across 30+ tables, we derive
-- the column from the already-authoritative session setting.
--
-- Semantics mirror the RLS predicate from 0020: app.livemode='off' → test;
-- unset / 'on' / any other value → live. This keeps the "forgot to propagate"
-- fallback safely pointing at production.
--
-- TxBypass transactions do not set app.livemode. A bypass-mode INSERT that
-- wants test-mode data must either SET LOCAL app.livemode='off' before the
-- insert, or the caller must route through a TxTenant with WithLivemode.
-- The trigger always defers to the session — explicit NEW.livemode values
-- in an INSERT statement are intentionally overwritten so a buggy producer
-- can't poison a partition.

CREATE OR REPLACE FUNCTION set_livemode_from_session() RETURNS trigger AS $$
BEGIN
    NEW.livemode := (current_setting('app.livemode', true) IS DISTINCT FROM 'off');
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

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
        EXECUTE format(
            'CREATE TRIGGER set_livemode
             BEFORE INSERT ON %I
             FOR EACH ROW EXECUTE FUNCTION set_livemode_from_session()',
            tbl
        );
    END LOOP;
END $$;
