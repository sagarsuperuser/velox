-- Audit read-path index fix (audit e2e 2026-07-13; ADR-089 arc, PR2).
--
-- audit.Query now carries explicit tenant_id + livemode predicates (the RLS
-- policy's bypass-GUC OR-arm references no columns, so RLS quals alone can
-- never drive an index — every audit read was a full multi-tenant seq scan).
-- Rebuild the read index as (tenant_id, livemode, created_at DESC, id DESC):
-- livemode is now an explicit equality qual, and the id tiebreaker keeps the
-- (created_at, id) tuple-seek cursor on the index instead of re-sorting
-- microsecond ties.
--
-- (0001 originally created a byte-identical duplicate of this index,
-- idx_audit_log_tenant; migration 0030 already dropped it.)
DROP INDEX IF EXISTS idx_audit_log_created;
CREATE INDEX idx_audit_log_tenant_read ON audit_log (tenant_id, livemode, created_at DESC, id DESC);
