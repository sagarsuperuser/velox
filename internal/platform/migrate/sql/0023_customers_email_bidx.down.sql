DROP INDEX IF EXISTS idx_customers_email_bidx;
ALTER TABLE customers DROP COLUMN IF EXISTS email_bidx;
