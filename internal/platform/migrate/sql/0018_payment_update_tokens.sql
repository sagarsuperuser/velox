CREATE TABLE payment_update_tokens (
    id          TEXT PRIMARY KEY,
    tenant_id   TEXT NOT NULL REFERENCES tenants(id),
    customer_id TEXT NOT NULL REFERENCES customers(id),
    invoice_id  TEXT NOT NULL REFERENCES invoices(id),
    token_hash  TEXT NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL,
    used_at     TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_payment_tokens_hash ON payment_update_tokens (token_hash) WHERE used_at IS NULL;
CREATE INDEX idx_payment_tokens_expires ON payment_update_tokens (expires_at);

GRANT ALL ON TABLE payment_update_tokens TO velox_app;
