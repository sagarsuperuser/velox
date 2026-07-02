-- 0125: checkout_sessions — the dedup/claim ledger for hosted-invoice Stripe
-- Checkout sessions (ADR-068, audit HIGH #5).
--
-- Pre-fix, POST /v1/public/invoices/{token}/checkout minted a FRESH Stripe
-- session on every call with no idempotency key, no ExpiresAt, and no
-- expire-on-settle: a customer paying on two devices (or after an email
-- re-open) was charged twice, with the second charge visible only in Stripe.
--
-- Protocol (panel-consolidated, see plan §4.2):
--   * CLAIM-FIRST: a POST inserts an 'open' row with stripe_session_id NULL
--     inside a tx that re-verifies the invoice is payable (FOR SHARE); the
--     Stripe create runs post-commit with an idempotency key DERIVED FROM
--     id, so any re-drive (crash, concurrent loser) converges on ONE session.
--   * The partial unique index is the concurrent-double-POST guard: one open
--     row per invoice, ever.
--   * Exits from payable state close rows IN-TX at the store choke points
--     (markPaidReportingTransition, void/uncollectible, credit-apply-to-zero);
--     the Stripe-side expire is post-commit best-effort with expires_at as
--     the backstop (1h, enforced STRIPE-SIDE via the create params).
--
-- status: open (payable claim) | superseded (amount drift / time-expired,
-- replaced by a newer claim) | completed (checkout.session.completed truth-
-- sync) | invoice_settled (closed in-tx by an invoice exit from payable) |
-- expired (Stripe-side expire confirmed).
CREATE TABLE checkout_sessions (
    id                TEXT PRIMARY KEY,
    tenant_id         TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    invoice_id        TEXT NOT NULL REFERENCES invoices(id) ON DELETE CASCADE,
    livemode          BOOLEAN NOT NULL,
    stripe_session_id TEXT,
    url               TEXT,
    amount_cents      BIGINT NOT NULL,
    currency          TEXT NOT NULL,
    status            TEXT NOT NULL DEFAULT 'open'
        CHECK (status IN ('open', 'superseded', 'completed', 'invoice_settled', 'expired')),
    expires_at        TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- THE concurrent-double-POST guard: at most one open claim per invoice.
CREATE UNIQUE INDEX idx_checkout_sessions_one_open_per_invoice
    ON checkout_sessions (invoice_id)
    WHERE status = 'open';

-- Settle-time sweep + truth-sync lookups.
CREATE INDEX idx_checkout_sessions_invoice ON checkout_sessions (invoice_id);
CREATE INDEX idx_checkout_sessions_stripe_id ON checkout_sessions (stripe_session_id)
    WHERE stripe_session_id IS NOT NULL;

-- RLS: every tenant table gets ENABLE + FORCE + tenant_isolation (0006
-- principle, discovery-tested). Mode-aware shape (0020) — the table carries
-- livemode.
ALTER TABLE checkout_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE checkout_sessions FORCE  ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON checkout_sessions FOR ALL USING (
    current_setting('app.bypass_rls', true) = 'on'
    OR (
        tenant_id = current_setting('app.tenant_id', true)
        AND livemode = (current_setting('app.livemode', true) IS DISTINCT FROM 'off')
    )
);
