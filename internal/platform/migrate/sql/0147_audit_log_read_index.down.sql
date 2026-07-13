DROP INDEX IF EXISTS idx_audit_log_tenant_read;
-- Restore the schema as of 0146: only idx_audit_log_created exists there
-- (0001 created a duplicate idx_audit_log_tenant, but 0030 dropped it —
-- recreating it here would resurrect a schema that never existed at 0146).
CREATE INDEX idx_audit_log_created ON audit_log (tenant_id, created_at DESC);
