-- Outbound webhooks: tenant-registered endpoints + event delivery tracking.

CREATE TABLE webhook_endpoints (
    id              TEXT PRIMARY KEY DEFAULT 'vlx_whe_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id       TEXT NOT NULL REFERENCES tenants(id),
    url             TEXT NOT NULL,
    description     TEXT,
    secret          TEXT NOT NULL,
    events          JSONB NOT NULL DEFAULT '["*"]',
    active          BOOLEAN NOT NULL DEFAULT true,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE webhook_events (
    id              TEXT PRIMARY KEY DEFAULT 'vlx_whevt_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id       TEXT NOT NULL REFERENCES tenants(id),
    event_type      TEXT NOT NULL,
    payload         JSONB NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_webhook_events_tenant ON webhook_events (tenant_id, created_at DESC);

CREATE TABLE webhook_deliveries (
    id                  TEXT PRIMARY KEY DEFAULT 'vlx_whd_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id           TEXT NOT NULL REFERENCES tenants(id),
    webhook_endpoint_id TEXT NOT NULL REFERENCES webhook_endpoints(id),
    webhook_event_id    TEXT NOT NULL REFERENCES webhook_events(id),
    status              TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'succeeded', 'failed')),
    http_status_code    INT,
    response_body       TEXT,
    error_message       TEXT,
    attempt_count       INT NOT NULL DEFAULT 0,
    next_retry_at       TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at        TIMESTAMPTZ
);

CREATE INDEX idx_webhook_deliveries_pending ON webhook_deliveries (status, next_retry_at)
    WHERE status = 'pending';

ALTER TABLE webhook_endpoints ENABLE ROW LEVEL SECURITY;
ALTER TABLE webhook_endpoints FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON webhook_endpoints FOR ALL USING (
    current_setting('app.bypass_rls', true) = 'on'
    OR tenant_id = current_setting('app.tenant_id', true)
);

ALTER TABLE webhook_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE webhook_events FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON webhook_events FOR ALL USING (
    current_setting('app.bypass_rls', true) = 'on'
    OR tenant_id = current_setting('app.tenant_id', true)
);

ALTER TABLE webhook_deliveries ENABLE ROW LEVEL SECURITY;
ALTER TABLE webhook_deliveries FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON webhook_deliveries FOR ALL USING (
    current_setting('app.bypass_rls', true) = 'on'
    OR tenant_id = current_setting('app.tenant_id', true)
);

GRANT ALL ON TABLE webhook_endpoints TO velox_app;
GRANT ALL ON TABLE webhook_events TO velox_app;
GRANT ALL ON TABLE webhook_deliveries TO velox_app;
