-- audit_log: give the READ path indexes for the things operators actually filter by.
--
-- Migration 0147 fixed the catastrophic case (every audit read was a full scan
-- across ALL tenants, because the RLS policy's bypass-GUC OR-arm references no
-- columns and so defeated every index). It gave the LIST its index:
-- (tenant_id, livemode, created_at DESC, id DESC).
--
-- But nothing indexes what the operator FILTERS by. audit_log is append-only —
-- 0150 revoked DELETE and TRUNCATE from the runtime role, so the application can
-- never prune it — which means every one of these scans grows without bound and
-- can never be reclaimed. That is the whole reason these are worth fixing now
-- rather than "when a tenant gets big": there is no later cleanup.
--
-- Each index carries (created_at DESC, id DESC) as its tail so ONE index serves the
-- equality filter, the ORDER BY, AND the seek cursor — no Sort node, and deep
-- pagination stays O(log n). Same shape as 0148's clock index, for the same reason.
--
-- Write cost is three extra B-tree inserts per audit row. That is affordable
-- precisely because ADR-090 exempted machine ingest (usage events, LiteLLM spend)
-- from auditing: audit_log grows at OPERATOR-ACTION rate, not request rate.

-- Filter by action ("show me every void"). Also the DISTINCT that populates the
-- action dropdown, which previously had NO index containing `action` at all and
-- therefore heap-fetched the tenant's entire history on every page open.
CREATE INDEX idx_audit_log_action
    ON audit_log (tenant_id, livemode, action, created_at DESC, id DESC);

-- Filter by actor ("everything this operator did"). The first question asked after
-- an incident, and it had no supporting index.
CREATE INDEX idx_audit_log_actor
    ON audit_log (tenant_id, livemode, actor_id, created_at DESC, id DESC);

-- Filter by resource_id ALONE — "the history of THIS invoice" — which is what the
-- entity detail pages link to. 0001's idx_audit_log_resource is
-- (tenant_id, resource_type, resource_id): resource_id is the THIRD column, so a
-- resource_id-only query cannot use it (Postgres 16 has no btree skip scan), and it
-- has no livemode column, so it heap-fetches to re-check the mode anyway.
CREATE INDEX idx_audit_log_resource_id
    ON audit_log (tenant_id, livemode, resource_id, created_at DESC, id DESC);

-- The resource_type dropdown's DISTINCT, and the (type + id) pair filter. Replaces
-- 0001's mode-blind idx_audit_log_resource, which is now redundant: this index has
-- the same leading columns plus livemode, so every query the old one served is
-- served better here.
CREATE INDEX idx_audit_log_resource_v2
    ON audit_log (tenant_id, livemode, resource_type, resource_id, created_at DESC, id DESC);

DROP INDEX IF EXISTS idx_audit_log_resource;
