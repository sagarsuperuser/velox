-- Audit-log request context: populate request_id (chi per-request UUID) on
-- every row so a row can be joined back to structured server logs, and to a
-- customer-reported Velox-Request-Id header. Paired with ip_address (column
-- already existed but was never populated) now that the middleware extracts
-- the client IP from X-Forwarded-For / X-Real-IP / RemoteAddr.
ALTER TABLE audit_log ADD COLUMN request_id TEXT;

-- idx_audit_log_created and idx_audit_log_tenant were created with identical
-- definitions (tenant_id, created_at DESC). The duplicate costs an extra
-- write on every audit insert and buys nothing on reads — drop one.
DROP INDEX IF EXISTS idx_audit_log_tenant;
