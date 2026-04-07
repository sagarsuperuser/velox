-- Customer credits (prepaid balance) and billing metrics.

-- ---------------------------------------------------------------------------
-- Customer Credit Ledger
-- Event-sourced: every credit change is an immutable entry.
-- Balance = SUM(amount_cents) for a customer.
-- ---------------------------------------------------------------------------
CREATE TABLE customer_credit_ledger (
    id              TEXT PRIMARY KEY DEFAULT 'vlx_ccl_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id       TEXT NOT NULL REFERENCES tenants(id),
    customer_id     TEXT NOT NULL REFERENCES customers(id),
    entry_type      TEXT NOT NULL CHECK (entry_type IN ('grant', 'usage', 'expiry', 'adjustment')),
    amount_cents    BIGINT NOT NULL,
    balance_after   BIGINT NOT NULL,
    description     TEXT NOT NULL,
    invoice_id      TEXT,
    expires_at      TIMESTAMPTZ,
    metadata        JSONB NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_credit_ledger_customer ON customer_credit_ledger (tenant_id, customer_id, created_at);

ALTER TABLE customer_credit_ledger ENABLE ROW LEVEL SECURITY;
ALTER TABLE customer_credit_ledger FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON customer_credit_ledger FOR ALL USING (
    current_setting('app.bypass_rls', true) = 'on'
    OR tenant_id = current_setting('app.tenant_id', true)
);
GRANT ALL ON TABLE customer_credit_ledger TO velox_app;
