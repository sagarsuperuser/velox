-- RES-6: transactional outbox for outbound transactional email.
--
-- Today, producers fire emails inline (SMTP SendMail inside a goroutine) right
-- after the business-op commit. A crash between commit and SMTP response — or
-- an SMTP hiccup, or a relay that accepts-then-bounces — silently drops the
-- email. Customer never sees the invoice; we never know we failed to send.
--
-- This table makes email emission durable: producers insert a row (ideally in
-- the same tx as the invoice/dunning/receipt state change), and a background
-- dispatcher drains pending rows and calls the SMTP sender. Same ops model as
-- RES-1 (webhook_outbox): pending → dispatched on success, 15-attempt backoff
-- ramp ending in DLQ (status='failed') so the operator can see and requeue.
--
-- This is the queue, not the receipts log. One row per email_type × recipient.

CREATE TABLE email_outbox (
    id                 TEXT PRIMARY KEY DEFAULT 'vlx_emob_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id          TEXT NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
    email_type         TEXT NOT NULL,
    payload            JSONB NOT NULL,
    status             TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'dispatched', 'failed')),
    attempts           INT NOT NULL DEFAULT 0,
    next_attempt_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_error         TEXT,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    dispatched_at      TIMESTAMPTZ
);

-- Partial index: the dispatcher only ever selects pending rows whose
-- next_attempt_at has come due. A partial index keeps the working set small
-- regardless of how many historical dispatched/failed rows accumulate.
CREATE INDEX idx_email_outbox_pending
    ON email_outbox (next_attempt_at)
    WHERE status = 'pending';

-- Per-tenant visibility index for the operator UI (stuck/failed emails).
CREATE INDEX idx_email_outbox_tenant_status
    ON email_outbox (tenant_id, status, created_at DESC);

-- RLS: same tenant_id isolation as every other tenant-scoped table. The
-- dispatcher runs with TxBypass to scan across tenants.
ALTER TABLE email_outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE email_outbox FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON email_outbox FOR ALL USING (
    current_setting('app.bypass_rls', true) = 'on'
    OR tenant_id = current_setting('app.tenant_id', true)
);
