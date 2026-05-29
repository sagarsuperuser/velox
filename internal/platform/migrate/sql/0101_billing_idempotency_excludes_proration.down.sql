-- Restore the prior predicate (no proration exemption). Rollback only;
-- the prior shape conflates cycle + proration dedup as documented in
-- 0101.up.sql.

DROP INDEX IF EXISTS idx_invoices_billing_idempotency;
CREATE UNIQUE INDEX idx_invoices_billing_idempotency
    ON invoices (tenant_id, subscription_id, billing_period_start, billing_period_end)
    WHERE status <> 'voided';
