DROP INDEX IF EXISTS idx_invoices_stripe_invoice_id;
ALTER TABLE invoices DROP COLUMN IF EXISTS stripe_invoice_id;
