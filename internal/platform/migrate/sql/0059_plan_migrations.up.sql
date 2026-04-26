-- Plan migrations: operator-initiated bulk swaps of plan_id across many
-- subscriptions. Records every cohort migration so the dashboard can list
-- prior runs, link to the per-customer audit trail, and detect duplicate
-- commits via idempotency_key.
--
-- The actual subscription_item.plan_id mutations live on subscription_items
-- (see migration 0029); this table is the audit / history surface above
-- them. Per-customer detail is recorded in audit_log via subscription.plan_changed
-- entries, with the matching plan_migration_id in metadata.
--
-- See Week 6 deliverable in docs/90-day-plan.md.

CREATE TABLE plan_migrations (
    id                  TEXT PRIMARY KEY DEFAULT 'vlx_pmig_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id           TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    -- Mode partition. Default 'live' so untouched callers stay safe; the
    -- BEFORE INSERT trigger rewrites this to the session livemode.
    livemode            BOOLEAN NOT NULL DEFAULT true,
    -- Idempotency key supplied by the operator. UNIQUE per tenant so
    -- replays of the same commit return the prior migration without
    -- re-applying the swap. Deliberately UNIQUE on (tenant_id, key) and
    -- not just (key) so two tenants can independently use the same key.
    idempotency_key     TEXT NOT NULL,
    from_plan_id        TEXT NOT NULL REFERENCES plans(id) ON DELETE RESTRICT,
    to_plan_id          TEXT NOT NULL REFERENCES plans(id) ON DELETE RESTRICT,
    -- Customer filter snapshot — JSON describing the cohort selector
    -- used at commit time. Always-object idiom: {} for "all", or
    -- {"type":"ids","ids":[...]}, or {"type":"tag","value":"..."}.
    customer_filter     JSONB NOT NULL DEFAULT '{}'::jsonb,
    -- "immediate" swaps plan_id and stamps plan_changed_at; "next_period"
    -- sets pending_plan_id + pending_plan_effective_at on each item.
    effective           TEXT NOT NULL CHECK (effective IN ('immediate','next_period')),
    -- Cohort summary computed at commit. applied_count is the number of
    -- subscription_items whose plan_id was swapped (or scheduled).
    applied_count       INTEGER NOT NULL DEFAULT 0,
    -- Roll-up of estimated delta cents across the cohort, keyed by
    -- currency. Snapshot of the preview's totals[] at commit time so
    -- list views don't have to re-run the preview engine.
    -- Always-array shape: [{"currency":"USD","before_amount_cents":...,"after_amount_cents":...,"delta_amount_cents":...}]
    totals              JSONB NOT NULL DEFAULT '[]'::jsonb,
    applied_by          TEXT NOT NULL,                  -- actor_id from auth context (api_key id, user id, or 'system')
    applied_by_type     TEXT NOT NULL DEFAULT 'api_key', -- mirrors audit_log.actor_type
    -- Audit log entry the cohort summary was written to. Lets the UI
    -- jump from the migration row to the canonical audit trail.
    audit_log_id        TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, idempotency_key)
);

-- Hot read: list past migrations for a tenant in reverse chronological
-- order. The dashboard's recent-migrations sidebar is the dominant query.
CREATE INDEX idx_plan_migrations_tenant_created
    ON plan_migrations (tenant_id, created_at DESC);

-- Standard tenant + mode isolation. FORCE applies even to the table
-- owner so a misconfigured connection string can't bypass it.
ALTER TABLE plan_migrations ENABLE ROW LEVEL SECURITY;
ALTER TABLE plan_migrations FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON plan_migrations FOR ALL USING (
    current_setting('app.bypass_rls', true) = 'on'
    OR (
        tenant_id = current_setting('app.tenant_id', true)
        AND livemode = (current_setting('app.livemode', true) IS DISTINCT FROM 'off')
    )
);

GRANT ALL ON TABLE plan_migrations TO velox_app;

-- Wire the BEFORE INSERT livemode trigger from migration 0021 so a
-- TxTenant in test-mode lands rows with livemode=false without callers
-- needing to set the column explicitly.
CREATE TRIGGER set_livemode
    BEFORE INSERT ON plan_migrations
    FOR EACH ROW EXECUTE FUNCTION set_livemode_from_session();
