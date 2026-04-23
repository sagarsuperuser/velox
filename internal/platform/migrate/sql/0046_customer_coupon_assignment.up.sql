-- Customer-scoped coupon assignment (Stripe's customer.discount primitive).
--
-- Dedicated table instead of overloading coupon_redemptions with the
-- "subscription_id IS NULL AND invoice_id IS NULL" sentinel — that pattern
-- conflated two distinct concepts (the attachment vs the per-invoice use)
-- into one row. Separating them means:
--
--   - The schema expresses its own invariants. At most one live assignment
--     per customer is enforced at the DB layer via a partial UNIQUE index,
--     not by the service's list-then-insert dance.
--   - Idempotency replay for attach is scoped to this table rather than
--     sharing coupon_redemptions.idempotency_key with subscription / invoice
--     redeems.
--   - Future fields (schedule window, auto-renew, etc.) land here without
--     polluting the redemption type.
--
-- The billing engine continues to advance periods_applied per invoice so
-- duration-limited coupons (once / repeating N) exhaust on schedule — but
-- now it bumps customer_discounts.periods_applied, not a redemption's.
-- Revocation sets revoked_at (partial UNIQUE filters voided rows out, so
-- the customer is free to re-attach a different coupon immediately).

CREATE TABLE customer_discounts (
    id              TEXT PRIMARY KEY DEFAULT 'vlx_cud_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id       TEXT NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
    customer_id     TEXT NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    coupon_id       TEXT NOT NULL REFERENCES coupons(id) ON DELETE RESTRICT,
    periods_applied INTEGER NOT NULL DEFAULT 0 CHECK (periods_applied >= 0),
    idempotency_key TEXT,
    metadata        JSONB NOT NULL DEFAULT '{}'::jsonb,
    revoked_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- At most one live (non-revoked) discount per customer. A concurrent attach
-- race loses on the unique violation and the service translates it into
-- CodeAlreadyAssigned — replaces the pre-check/insert TOCTOU in the old
-- "list then insert" flow.
CREATE UNIQUE INDEX idx_customer_discounts_active
    ON customer_discounts (tenant_id, customer_id)
    WHERE revoked_at IS NULL;

-- Idempotency: same key, same row. Partial so rows without a key don't
-- collide — mirrors coupon_redemptions.idempotency_key shape.
CREATE UNIQUE INDEX idx_customer_discounts_idempotency
    ON customer_discounts (tenant_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;

-- Coupon lookups for the reverse direction (how many customers hold this
-- coupon via assignment). Used by the coupon detail page once the UI grows
-- the "standing assignments" tab.
CREATE INDEX idx_customer_discounts_coupon
    ON customer_discounts (tenant_id, coupon_id);

-- RLS: standard tenant isolation. bypass_rls is the escape hatch for the
-- background engine worker that scans across tenants.
ALTER TABLE customer_discounts ENABLE ROW LEVEL SECURITY;
ALTER TABLE customer_discounts FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON customer_discounts FOR ALL USING (
    current_setting('app.bypass_rls', true) = 'on'
    OR tenant_id = current_setting('app.tenant_id', true)
);
