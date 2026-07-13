-- Audit read-path index fix (audit e2e 2026-07-13; ADR-089 arc, PR2).
--
-- idx_audit_log_tenant was a byte-identical duplicate of idx_audit_log_created
-- (both (tenant_id, created_at DESC), created side by side in 0001) — every
-- audit INSERT paid a third needless index maintenance for zero read benefit.
--
-- The survivor is rebuilt as (tenant_id, livemode, created_at DESC, id DESC):
-- audit.Query now carries explicit tenant_id + livemode predicates (the RLS
-- policy's bypass-GUC OR-arm references no columns, so RLS quals alone can
-- never drive an index — every audit read was a full multi-tenant seq scan),
-- and orders by (created_at DESC, id DESC) with a matching tuple-seek cursor;
-- the id tiebreaker column keeps the seek on the index instead of re-sorting
-- microsecond ties.
DROP INDEX IF EXISTS idx_audit_log_tenant;
DROP INDEX IF EXISTS idx_audit_log_created;
CREATE INDEX idx_audit_log_tenant_read ON audit_log (tenant_id, livemode, created_at DESC, id DESC);
