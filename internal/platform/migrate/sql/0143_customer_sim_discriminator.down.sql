DROP INDEX IF EXISTS idx_customers_live_created;
ALTER TABLE customers DROP CONSTRAINT IF EXISTS customers_sim_not_live;
ALTER TABLE customers DROP COLUMN IF EXISTS is_simulated;
ALTER TABLE customers DROP COLUMN IF EXISTS sim_clock_id;
