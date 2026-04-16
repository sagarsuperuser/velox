-- 0019: Enterprise hardening — indexes, idempotency, rate limiting, audit reliability
--
-- Addresses:
--   1. Missing composite indexes on high-traffic query paths
--   2. Invoice generation idempotency (prevent double-billing)
--   3. PostgreSQL-backed distributed rate limiting
--   4. Tax rate precision (float64 → basis points)
--   5. Auto-charge tracking for reliable payment retry

------------------------------------------------------------------------
-- 1. PERFORMANCE INDEXES
------------------------------------------------------------------------

-- Invoice lookups by customer (dashboard, billing history)
CREATE INDEX IF NOT EXISTS idx_invoices_tenant_customer
    ON invoices (tenant_id, customer_id);

-- Invoice filtering by status (list pending, list paid, etc.)
CREATE INDEX IF NOT EXISTS idx_invoices_tenant_status
    ON invoices (tenant_id, status);

-- Invoice filtering by payment status (find failed payments)
CREATE INDEX IF NOT EXISTS idx_invoices_tenant_payment_status
    ON invoices (tenant_id, payment_status);

-- Invoice due date lookups (approaching due, overdue queries)
CREATE INDEX IF NOT EXISTS idx_invoices_tenant_due_at
    ON invoices (tenant_id, due_at)
    WHERE due_at IS NOT NULL;

-- Invoice subscription lookups
CREATE INDEX IF NOT EXISTS idx_invoices_tenant_subscription
    ON invoices (tenant_id, subscription_id);

-- Invoice created_at for default sort order
CREATE INDEX IF NOT EXISTS idx_invoices_tenant_created
    ON invoices (tenant_id, created_at DESC);

-- Line items by invoice (fetched on every invoice detail view)
CREATE INDEX IF NOT EXISTS idx_line_items_invoice
    ON invoice_line_items (invoice_id);

-- Dunning scheduler: find runs due for processing
CREATE INDEX IF NOT EXISTS idx_dunning_runs_due
    ON invoice_dunning_runs (tenant_id, state, next_action_at)
    WHERE state IN ('active', 'escalated');

-- Dunning by invoice (lookup existing run for an invoice)
CREATE INDEX IF NOT EXISTS idx_dunning_runs_invoice
    ON invoice_dunning_runs (tenant_id, invoice_id);

-- Usage events: aggregate per customer per meter per period
CREATE INDEX IF NOT EXISTS idx_usage_events_aggregate
    ON usage_events (tenant_id, customer_id, meter_id, timestamp);

-- Webhook deliveries: retry worker picks pending deliveries
CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_pending
    ON webhook_deliveries (status, next_retry_at)
    WHERE status = 'pending' AND next_retry_at IS NOT NULL;

-- Audit log: query by resource
CREATE INDEX IF NOT EXISTS idx_audit_log_resource
    ON audit_log (tenant_id, resource_type, resource_id);

-- Audit log: query by time range
CREATE INDEX IF NOT EXISTS idx_audit_log_created
    ON audit_log (tenant_id, created_at DESC);

-- Credit ledger: balance lookups per customer (index already exists from 0003)

-- Subscriptions: billing scheduler looks up due subscriptions
CREATE INDEX IF NOT EXISTS idx_subscriptions_next_billing
    ON subscriptions (next_billing_at, status)
    WHERE status = 'active';

------------------------------------------------------------------------
-- 2. INVOICE IDEMPOTENCY — prevent duplicate invoices per billing period
------------------------------------------------------------------------

-- Unique constraint: one invoice per subscription per billing period.
-- This prevents the billing engine from generating duplicate invoices
-- if RunCycle is called twice (crash + restart, scheduler overlap).
CREATE UNIQUE INDEX IF NOT EXISTS idx_invoices_billing_idempotency
    ON invoices (tenant_id, subscription_id, billing_period_start, billing_period_end)
    WHERE status != 'voided';

------------------------------------------------------------------------
-- 3. DISTRIBUTED RATE LIMITING — now uses Redis (no PostgreSQL table needed)
------------------------------------------------------------------------
-- Rate limiting moved to Redis for sub-millisecond enforcement.
-- Env var: REDIS_URL=redis://host:6379/0

------------------------------------------------------------------------
-- 4. TAX RATE PRECISION — convert float to basis points (integer)
------------------------------------------------------------------------

-- Add basis-point columns alongside existing float columns.
-- 1 basis point = 0.01%, so 18.5% = 1850 bp, 7.25% = 725 bp.

-- Tenant settings: default tax rate
ALTER TABLE tenant_settings
    ADD COLUMN IF NOT EXISTS tax_rate_bp INTEGER NOT NULL DEFAULT 0;

-- Backfill from existing float column: round(float * 100) = basis points
UPDATE tenant_settings SET tax_rate_bp = ROUND(tax_rate * 100)::INTEGER
    WHERE tax_rate_bp = 0 AND tax_rate > 0;

-- Invoices: tax rate snapshot
ALTER TABLE invoices
    ADD COLUMN IF NOT EXISTS tax_rate_bp INTEGER NOT NULL DEFAULT 0;

UPDATE invoices SET tax_rate_bp = ROUND(tax_rate * 100)::INTEGER
    WHERE tax_rate_bp = 0 AND tax_rate > 0;

-- Invoice line items: per-line tax rate
ALTER TABLE invoice_line_items
    ADD COLUMN IF NOT EXISTS tax_rate_bp INTEGER NOT NULL DEFAULT 0;

UPDATE invoice_line_items SET tax_rate_bp = ROUND(tax_rate * 100)::INTEGER
    WHERE tax_rate_bp = 0 AND tax_rate > 0;

-- Customer billing profiles: per-customer override
ALTER TABLE customer_billing_profiles
    ADD COLUMN IF NOT EXISTS tax_override_rate_bp INTEGER;

UPDATE customer_billing_profiles SET tax_override_rate_bp = ROUND(tax_override_rate * 100)::INTEGER
    WHERE tax_override_rate_bp IS NULL AND tax_override_rate IS NOT NULL;

-- Coupon: percent_off as basis points (50.5% = 5050)
ALTER TABLE coupons
    ADD COLUMN IF NOT EXISTS percent_off_bp INTEGER NOT NULL DEFAULT 0;

UPDATE coupons SET percent_off_bp = ROUND(percent_off * 100)::INTEGER
    WHERE percent_off_bp = 0 AND percent_off > 0;

------------------------------------------------------------------------
-- 5. AUTO-CHARGE TRACKING — reliable payment retry
------------------------------------------------------------------------

-- Track whether an invoice needs auto-charging so the scheduler can retry.
ALTER TABLE invoices
    ADD COLUMN IF NOT EXISTS auto_charge_pending BOOLEAN NOT NULL DEFAULT FALSE;

CREATE INDEX IF NOT EXISTS idx_invoices_auto_charge
    ON invoices (auto_charge_pending)
    WHERE auto_charge_pending = TRUE AND payment_status = 'pending';

------------------------------------------------------------------------
-- 6. API KEY SALT — prevent rainbow table attacks on key hashes
------------------------------------------------------------------------

ALTER TABLE api_keys
    ADD COLUMN IF NOT EXISTS key_salt TEXT NOT NULL DEFAULT '';
