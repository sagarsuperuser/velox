CREATE TABLE IF NOT EXISTS customer_dunning_overrides (
    customer_id TEXT NOT NULL REFERENCES customers(id),
    tenant_id   TEXT NOT NULL REFERENCES tenants(id),
    max_retry_attempts INT,
    grace_period_days  INT,
    final_action       TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, customer_id)
);

ALTER TABLE customer_dunning_overrides ENABLE ROW LEVEL SECURITY;
ALTER TABLE customer_dunning_overrides FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON customer_dunning_overrides FOR ALL USING (
    current_setting('app.bypass_rls', true) = 'on'
    OR tenant_id = current_setting('app.tenant_id', true)
);
GRANT ALL ON TABLE customer_dunning_overrides TO velox_app;
