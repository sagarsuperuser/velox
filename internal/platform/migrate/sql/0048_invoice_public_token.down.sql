DROP INDEX IF EXISTS idx_invoices_public_token;
ALTER TABLE invoices DROP COLUMN public_token;
