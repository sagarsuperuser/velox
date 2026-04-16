-- Idempotency key storage for safe retries on write endpoints.

CREATE TABLE idempotency_keys (
    key             TEXT NOT NULL,
    tenant_id       TEXT NOT NULL,
    http_method     TEXT NOT NULL,
    http_path       TEXT NOT NULL,
    status_code     INT NOT NULL,
    response_body   BYTEA NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at      TIMESTAMPTZ NOT NULL DEFAULT now() + INTERVAL '24 hours',
    PRIMARY KEY (tenant_id, key)
);

CREATE INDEX idx_idempotency_expires ON idempotency_keys (expires_at);

GRANT ALL ON TABLE idempotency_keys TO velox_app;
