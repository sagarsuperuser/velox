-- Restore bulk_actions table. Mirrors 0061_bulk_actions.up.sql verbatim
-- so this migration can run in any direction. The internal/bulkaction
-- package would need to be re-introduced separately to actually use it.

CREATE TABLE bulk_actions (
    id                  TEXT PRIMARY KEY DEFAULT 'vlx_bact_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id           TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    livemode            BOOLEAN NOT NULL DEFAULT true,
    idempotency_key     TEXT NOT NULL,
    action_type         TEXT NOT NULL CHECK (action_type IN ('apply_coupon','schedule_cancel')),
    customer_filter     JSONB NOT NULL DEFAULT '{}'::jsonb,
    params              JSONB NOT NULL DEFAULT '{}'::jsonb,
    status              TEXT NOT NULL DEFAULT 'pending'
                        CHECK (status IN ('pending','running','completed','partial','failed')),
    target_count        INTEGER NOT NULL DEFAULT 0,
    succeeded_count     INTEGER NOT NULL DEFAULT 0,
    failed_count        INTEGER NOT NULL DEFAULT 0,
    errors              JSONB,
    created_by          TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at        TIMESTAMPTZ,
    UNIQUE (tenant_id, idempotency_key)
);

CREATE INDEX idx_bulk_actions_tenant_created
    ON bulk_actions (tenant_id, created_at DESC);
CREATE INDEX idx_bulk_actions_tenant_status
    ON bulk_actions (tenant_id, status);
CREATE INDEX idx_bulk_actions_tenant_action_type
    ON bulk_actions (tenant_id, action_type);

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

CREATE TRIGGER set_livemode
    BEFORE INSERT ON bulk_actions
    FOR EACH ROW EXECUTE FUNCTION set_livemode_from_session();
