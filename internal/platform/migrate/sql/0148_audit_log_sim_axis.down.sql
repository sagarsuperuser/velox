DROP INDEX IF EXISTS idx_audit_log_clock;
ALTER TABLE audit_log DROP COLUMN IF EXISTS test_clock_id;
ALTER TABLE audit_log DROP COLUMN IF EXISTS sim_effective_at;
