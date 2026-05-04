DROP INDEX IF EXISTS idx_invoices_tax_retry_due;
ALTER TABLE invoices DROP COLUMN tax_next_retry_at;
