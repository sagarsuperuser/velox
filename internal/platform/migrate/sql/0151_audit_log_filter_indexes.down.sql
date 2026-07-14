-- Restore 0001's index before dropping the one that replaced it, so the down
-- migration never leaves the resource lookup with no index at all.
CREATE INDEX IF NOT EXISTS idx_audit_log_resource ON audit_log (tenant_id, resource_type, resource_id);

DROP INDEX IF EXISTS idx_audit_log_resource_v2;
DROP INDEX IF EXISTS idx_audit_log_resource_id;
DROP INDEX IF EXISTS idx_audit_log_actor;
DROP INDEX IF EXISTS idx_audit_log_action;
