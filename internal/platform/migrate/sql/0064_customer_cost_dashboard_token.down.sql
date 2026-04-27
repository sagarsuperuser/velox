DROP INDEX IF EXISTS idx_customers_cost_dashboard_token;
ALTER TABLE customers DROP COLUMN IF EXISTS cost_dashboard_token;
