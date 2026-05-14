-- Restore billing_alerts + billing_alert_triggers tables.
-- Mirrors 0057_billing_alerts.up.sql verbatim.

CREATE TABLE billing_alerts (
    id                     TEXT PRIMARY KEY DEFAULT 'vlx_alrt_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id              TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    livemode               BOOLEAN NOT NULL DEFAULT true,
    customer_id            TEXT NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    title                  TEXT NOT NULL,
    meter_id               TEXT REFERENCES meters(id) ON DELETE SET NULL,
    dimensions             JSONB NOT NULL DEFAULT '{}'::jsonb,
    threshold_amount_cents BIGINT,
    threshold_quantity     NUMERIC(38,12),
    recurrence             TEXT NOT NULL CHECK (recurrence IN ('one_time','per_period')),
    status                 TEXT NOT NULL DEFAULT 'active'
                                CHECK (status IN ('active','triggered','triggered_for_period','archived')),
    last_triggered_at      TIMESTAMPTZ,
    last_period_start      TIMESTAMPTZ,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ( (threshold_amount_cents IS NOT NULL)::int + (threshold_quantity IS NOT NULL)::int = 1 )
);

CREATE INDEX idx_billing_alerts_tenant_customer
    ON billing_alerts (tenant_id, customer_id, status);

CREATE INDEX idx_billing_alerts_evaluator
    ON billing_alerts (status)
    WHERE status IN ('active','triggered_for_period');

ALTER TABLE billing_alerts ENABLE ROW LEVEL SECURITY;
ALTER TABLE billing_alerts FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON billing_alerts FOR ALL USING (
    current_setting('app.bypass_rls', true) = 'on'
    OR (
        tenant_id = current_setting('app.tenant_id', true)
        AND livemode = (current_setting('app.livemode', true) IS DISTINCT FROM 'off')
    )
);

GRANT ALL ON TABLE billing_alerts TO velox_app;

CREATE TABLE billing_alert_triggers (
    id                    TEXT PRIMARY KEY DEFAULT 'vlx_atrg_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id             TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    livemode              BOOLEAN NOT NULL DEFAULT true,
    alert_id              TEXT NOT NULL REFERENCES billing_alerts(id) ON DELETE CASCADE,
    period_from           TIMESTAMPTZ NOT NULL,
    period_to             TIMESTAMPTZ NOT NULL,
    observed_amount_cents BIGINT NOT NULL,
    observed_quantity     NUMERIC(38,12) NOT NULL DEFAULT 0,
    currency              TEXT NOT NULL,
    triggered_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (alert_id, period_from)
);

CREATE INDEX idx_billing_alert_triggers_alert
    ON billing_alert_triggers (alert_id, triggered_at DESC);

ALTER TABLE billing_alert_triggers ENABLE ROW LEVEL SECURITY;
ALTER TABLE billing_alert_triggers FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON billing_alert_triggers FOR ALL USING (
    current_setting('app.bypass_rls', true) = 'on'
    OR (
        tenant_id = current_setting('app.tenant_id', true)
        AND livemode = (current_setting('app.livemode', true) IS DISTINCT FROM 'off')
    )
);

GRANT ALL ON TABLE billing_alert_triggers TO velox_app;

CREATE TRIGGER set_livemode
    BEFORE INSERT ON billing_alerts
    FOR EACH ROW EXECUTE FUNCTION set_livemode_from_session();

CREATE TRIGGER set_livemode
    BEFORE INSERT ON billing_alert_triggers
    FOR EACH ROW EXECUTE FUNCTION set_livemode_from_session();
