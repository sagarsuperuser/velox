CREATE INDEX IF NOT EXISTS idx_audit_log_tenant ON audit_log (tenant_id, created_at DESC);
ALTER TABLE audit_log DROP COLUMN IF EXISTS request_id;
