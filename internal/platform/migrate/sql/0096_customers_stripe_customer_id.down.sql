DROP INDEX IF EXISTS idx_customers_stripe_customer_id_unique;
ALTER TABLE customers DROP COLUMN IF EXISTS stripe_customer_id;
