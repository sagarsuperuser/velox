-- Drop the BEFORE INSERT livemode-autoset triggers and the shared function.
-- Post-revert, INSERTs revert to the column DEFAULT (true) unless callers
-- explicitly pass livemode.

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
        EXECUTE format('DROP TRIGGER IF EXISTS set_livemode ON %I', tbl);
    END LOOP;
END $$;

DROP FUNCTION IF EXISTS set_livemode_from_session();
