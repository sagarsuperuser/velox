-- RES-1: transactional outbox for outbound webhook events.
--
-- Today, producers fire events via `go func() { events.Dispatch(...) }()`.
-- A crash between the business-op commit and that goroutine running silently
-- loses the event. The outbox makes emission durable: producers insert a row
-- here (eventually in the same tx as their state change); a background
-- dispatcher drains pending rows and calls the existing delivery pipeline.
--
-- This table is the queue, not the delivery log — that remains in
-- webhook_deliveries. A single outbox row fans out to N deliveries.

CREATE TABLE webhook_outbox (
    id                 TEXT PRIMARY KEY DEFAULT 'vlx_whob_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id          TEXT NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
    event_type         TEXT NOT NULL,
    payload            JSONB NOT NULL,
    status             TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'dispatched', 'failed')),
    attempts           INT NOT NULL DEFAULT 0,
    next_attempt_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_error         TEXT,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    dispatched_at      TIMESTAMPTZ
);

-- Partial index: the dispatcher only ever selects rows in ('pending') whose
-- next_attempt_at has come due. A partial index keeps the working set small
-- regardless of how many historical dispatched/failed rows accumulate.
CREATE INDEX idx_webhook_outbox_pending
    ON webhook_outbox (next_attempt_at)
    WHERE status = 'pending';

-- Per-tenant visibility index for the operator UI (stuck/failed events).
CREATE INDEX idx_webhook_outbox_tenant_status
    ON webhook_outbox (tenant_id, status, created_at DESC);

-- RLS: standard tenant_id isolation, same predicate as every other tenant
-- table. The dispatcher runs with TxBypass to scan across tenants.
ALTER TABLE webhook_outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE webhook_outbox FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON webhook_outbox FOR ALL USING (
    current_setting('app.bypass_rls', true) = 'on'
    OR tenant_id = current_setting('app.tenant_id', true)
);
