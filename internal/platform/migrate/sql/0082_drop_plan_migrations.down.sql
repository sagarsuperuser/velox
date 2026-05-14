-- Restore plan_migrations table. Mirrors 0059_plan_migrations.up.sql verbatim.

CREATE TABLE plan_migrations (
    id                  TEXT PRIMARY KEY DEFAULT 'vlx_pmig_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id           TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    livemode            BOOLEAN NOT NULL DEFAULT true,
    idempotency_key     TEXT NOT NULL,
    from_plan_id        TEXT NOT NULL REFERENCES plans(id) ON DELETE RESTRICT,
    to_plan_id          TEXT NOT NULL REFERENCES plans(id) ON DELETE RESTRICT,
    customer_filter     JSONB NOT NULL DEFAULT '{}'::jsonb,
    effective           TEXT NOT NULL CHECK (effective IN ('immediate','next_period')),
    applied_count       INTEGER NOT NULL DEFAULT 0,
    totals              JSONB NOT NULL DEFAULT '[]'::jsonb,
    applied_by          TEXT NOT NULL,
    applied_by_type     TEXT NOT NULL DEFAULT 'api_key',
    audit_log_id        TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, idempotency_key)
);

CREATE INDEX idx_plan_migrations_tenant_created
    ON plan_migrations (tenant_id, created_at DESC);

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

CREATE TRIGGER set_livemode
    BEFORE INSERT ON plan_migrations
    FOR EACH ROW EXECUTE FUNCTION set_livemode_from_session();
