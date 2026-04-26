-- Bulk actions: operator-initiated cohort operations across many customers.
-- v1 supports two action types:
--   - apply_coupon     : attach a coupon to every customer in the cohort
--   - schedule_cancel  : schedule cancellation for every active subscription
--                        in the cohort
--
-- Mirrors plan_migrations (migration 0059) -- one audit-history row per
-- operator-initiated cohort run, with a UNIQUE (tenant_id, idempotency_key)
-- constraint so retries return the prior row instead of re-running.
--
-- Per-customer mutations are recorded in audit_log via per-target entries
-- with the matching bulk_action_id in metadata, plus a single cohort
-- summary entry on the bulk_action_id row itself.
--
-- See Week 7 deliverable in docs/90-day-plan.md.

CREATE TABLE bulk_actions (
    id                  TEXT PRIMARY KEY DEFAULT 'vlx_bact_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id           TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    -- Mode partition. Default 'live' so untouched callers stay safe; the
    -- BEFORE INSERT trigger rewrites this to the session livemode.
    livemode            BOOLEAN NOT NULL DEFAULT true,
    -- Idempotency key supplied by the operator. UNIQUE per tenant so
    -- replays of the same commit return the prior bulk action without
    -- re-applying the cohort. Deliberately UNIQUE on (tenant_id, key) so
    -- two tenants can independently use the same key.
    idempotency_key     TEXT NOT NULL,
    -- Action type. v1 enums: apply_coupon, schedule_cancel. Future types
    -- (release_payment_hold, etc.) extend the CHECK constraint via a
    -- follow-up migration.
    action_type         TEXT NOT NULL CHECK (action_type IN ('apply_coupon','schedule_cancel')),
    -- Customer filter snapshot -- JSON describing the cohort selector
    -- used at commit time. Always-object idiom: {"type":"all"} or
    -- {"type":"ids","ids":[...]}, or {"type":"tag","value":"..."}.
    customer_filter     JSONB NOT NULL DEFAULT '{}'::jsonb,
    -- Action-type-specific parameters (e.g. {"coupon_code":"SUMMER20",
    -- "idempotency_prefix":"bact_..."} or {"at_period_end":true}).
    -- Always-object shape so list views can render param details without
    -- nil-guarding.
    params              JSONB NOT NULL DEFAULT '{}'::jsonb,
    -- pending -> running -> completed | partial | failed
    status              TEXT NOT NULL DEFAULT 'pending'
                        CHECK (status IN ('pending','running','completed','partial','failed')),
    -- Cohort counters: target = picked customers, succeeded = applied,
    -- failed = rejected (per-customer error captured in errors[]).
    target_count        INTEGER NOT NULL DEFAULT 0,
    succeeded_count     INTEGER NOT NULL DEFAULT 0,
    failed_count        INTEGER NOT NULL DEFAULT 0,
    -- Per-target failure messages. Always-array shape for list rendering.
    -- [{"customer_id":"vlx_cus_...","error":"..."}, ...]
    errors              JSONB,
    -- Actor who created this bulk action. Same shape as audit_log.actor_id.
    created_by          TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at        TIMESTAMPTZ,
    UNIQUE (tenant_id, idempotency_key)
);

-- Hot read: list past bulk actions for a tenant in reverse chronological
-- order, optionally filtered by status / action_type. The dashboard's
-- /admin/bulk-actions page is the dominant query.
CREATE INDEX idx_bulk_actions_tenant_created
    ON bulk_actions (tenant_id, created_at DESC);

CREATE INDEX idx_bulk_actions_tenant_status
    ON bulk_actions (tenant_id, status);

CREATE INDEX idx_bulk_actions_tenant_action_type
    ON bulk_actions (tenant_id, action_type);

-- Standard tenant + mode isolation. FORCE applies even to the table
-- owner so a misconfigured connection string can't bypass it.
ALTER TABLE bulk_actions ENABLE ROW LEVEL SECURITY;
ALTER TABLE bulk_actions FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON bulk_actions FOR ALL USING (
    current_setting('app.bypass_rls', true) = 'on'
    OR (
        tenant_id = current_setting('app.tenant_id', true)
        AND livemode = (current_setting('app.livemode', true) IS DISTINCT FROM 'off')
    )
);

GRANT ALL ON TABLE bulk_actions TO velox_app;

-- Wire the BEFORE INSERT livemode trigger from migration 0021 so a
-- TxTenant in test-mode lands rows with livemode=false without callers
-- needing to set the column explicitly.
CREATE TRIGGER set_livemode
    BEFORE INSERT ON bulk_actions
    FOR EACH ROW EXECUTE FUNCTION set_livemode_from_session();
