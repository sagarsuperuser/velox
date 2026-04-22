DROP INDEX IF EXISTS idx_invoices_tax_status_pending;

ALTER TABLE invoices
    DROP COLUMN IF EXISTS tax_status,
    DROP COLUMN IF EXISTS tax_deferred_at,
    DROP COLUMN IF EXISTS tax_retry_count,
    DROP COLUMN IF EXISTS tax_pending_reason;

ALTER TABLE tenant_settings
    DROP COLUMN IF EXISTS tax_on_failure;
