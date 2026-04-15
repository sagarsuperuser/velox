-- 0019: Rollback enterprise hardening
--
-- Reverses: indexes, tax_rate_bp columns, auto_charge_pending, key_salt
-- NOTE: rate_limit_buckets table was never created (moved to Redis), nothing to drop.

------------------------------------------------------------------------
-- 6. API KEY SALT — remove column
------------------------------------------------------------------------

ALTER TABLE api_keys
    DROP COLUMN IF EXISTS key_salt;

------------------------------------------------------------------------
-- 5. AUTO-CHARGE TRACKING — remove column and index
------------------------------------------------------------------------

DROP INDEX IF EXISTS idx_invoices_auto_charge;

ALTER TABLE invoices
    DROP COLUMN IF EXISTS auto_charge_pending;

------------------------------------------------------------------------
-- 4. TAX RATE PRECISION — remove basis-point columns
------------------------------------------------------------------------

ALTER TABLE coupons
    DROP COLUMN IF EXISTS percent_off_bp;

ALTER TABLE customer_billing_profiles
    DROP COLUMN IF EXISTS tax_override_rate_bp;

ALTER TABLE invoice_line_items
    DROP COLUMN IF EXISTS tax_rate_bp;

ALTER TABLE invoices
    DROP COLUMN IF EXISTS tax_rate_bp;

ALTER TABLE tenant_settings
    DROP COLUMN IF EXISTS tax_rate_bp;

------------------------------------------------------------------------
-- 3. DISTRIBUTED RATE LIMITING — nothing to drop (was Redis-only)
------------------------------------------------------------------------

------------------------------------------------------------------------
-- 2. INVOICE IDEMPOTENCY — remove unique index
------------------------------------------------------------------------

DROP INDEX IF EXISTS idx_invoices_billing_idempotency;

------------------------------------------------------------------------
-- 1. PERFORMANCE INDEXES — drop all indexes created in the up migration
------------------------------------------------------------------------

DROP INDEX IF EXISTS idx_subscriptions_next_billing;
DROP INDEX IF EXISTS idx_credit_ledger_customer;
DROP INDEX IF EXISTS idx_audit_log_created;
DROP INDEX IF EXISTS idx_audit_log_resource;
DROP INDEX IF EXISTS idx_webhook_deliveries_pending;
DROP INDEX IF EXISTS idx_usage_events_aggregate;
DROP INDEX IF EXISTS idx_dunning_runs_invoice;
DROP INDEX IF EXISTS idx_dunning_runs_due;
DROP INDEX IF EXISTS idx_line_items_invoice;
DROP INDEX IF EXISTS idx_invoices_tenant_created;
DROP INDEX IF EXISTS idx_invoices_tenant_subscription;
DROP INDEX IF EXISTS idx_invoices_tenant_due_at;
DROP INDEX IF EXISTS idx_invoices_tenant_payment_status;
DROP INDEX IF EXISTS idx_invoices_tenant_status;
DROP INDEX IF EXISTS idx_invoices_tenant_customer;
