DROP INDEX IF EXISTS idx_invoices_tax_pending;
ALTER TABLE invoices DROP COLUMN IF EXISTS tax_error_code;
