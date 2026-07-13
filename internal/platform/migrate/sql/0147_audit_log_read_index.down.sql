DROP INDEX IF EXISTS idx_audit_log_tenant_read;
-- Recreate both 0001 originals, duplicate included — down migrations restore
-- the prior schema exactly.
CREATE INDEX idx_audit_log_created ON audit_log (tenant_id, created_at DESC);
CREATE INDEX idx_audit_log_tenant ON audit_log (tenant_id, created_at DESC);
