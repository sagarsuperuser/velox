-- Audit log and tenant settings.

-- ---------------------------------------------------------------------------
-- Audit Log
-- Immutable append-only log of sensitive operations.
-- ---------------------------------------------------------------------------
CREATE TABLE audit_log (
    id              TEXT PRIMARY KEY DEFAULT 'vlx_aud_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id       TEXT NOT NULL REFERENCES tenants(id),
    actor_type      TEXT NOT NULL CHECK (actor_type IN ('api_key', 'user', 'system')),
    actor_id        TEXT NOT NULL,
    action          TEXT NOT NULL,
    resource_type   TEXT NOT NULL,
    resource_id     TEXT NOT NULL,
    metadata        JSONB NOT NULL DEFAULT '{}',
    ip_address      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_audit_log_tenant ON audit_log (tenant_id, created_at DESC);
CREATE INDEX idx_audit_log_resource ON audit_log (tenant_id, resource_type, resource_id);

ALTER TABLE audit_log ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_log FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON audit_log FOR ALL USING (
    current_setting('app.bypass_rls', true) = 'on'
    OR tenant_id = current_setting('app.tenant_id', true)
);
GRANT ALL ON TABLE audit_log TO velox_app;

-- ---------------------------------------------------------------------------
-- Tenant Settings
-- ---------------------------------------------------------------------------
CREATE TABLE tenant_settings (
    tenant_id           TEXT PRIMARY KEY REFERENCES tenants(id),
    default_currency    TEXT NOT NULL DEFAULT 'USD',
    timezone            TEXT NOT NULL DEFAULT 'UTC',
    invoice_prefix      TEXT NOT NULL DEFAULT 'VLX',
    invoice_next_seq    INT NOT NULL DEFAULT 1,
    net_payment_terms   INT NOT NULL DEFAULT 30,
    company_name        TEXT,
    company_address     TEXT,
    company_email       TEXT,
    company_phone       TEXT,
    logo_url            TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

GRANT ALL ON TABLE tenant_settings TO velox_app;
