-- Tier 1 enterprise features: price overrides, credit notes, prorated invoicing.

-- ---------------------------------------------------------------------------
-- Per-Customer Price Overrides
-- Allows overriding a rating rule's pricing for a specific customer.
-- When billing, the engine checks for an override before using the default rule.
-- ---------------------------------------------------------------------------
CREATE TABLE customer_price_overrides (
    id                      TEXT PRIMARY KEY DEFAULT 'vlx_cpo_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id               TEXT NOT NULL REFERENCES tenants(id),
    customer_id             TEXT NOT NULL REFERENCES customers(id),
    rating_rule_version_id  TEXT NOT NULL REFERENCES rating_rule_versions(id),
    mode                    TEXT NOT NULL CHECK (mode IN ('flat', 'graduated', 'package')),
    flat_amount_cents       BIGINT NOT NULL DEFAULT 0,
    graduated_tiers         JSONB NOT NULL DEFAULT '[]',
    package_size            BIGINT NOT NULL DEFAULT 0,
    package_amount_cents    BIGINT NOT NULL DEFAULT 0,
    overage_unit_amount_cents BIGINT NOT NULL DEFAULT 0,
    reason                  TEXT,
    active                  BOOLEAN NOT NULL DEFAULT true,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, customer_id, rating_rule_version_id)
);

ALTER TABLE customer_price_overrides ENABLE ROW LEVEL SECURITY;
ALTER TABLE customer_price_overrides FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON customer_price_overrides FOR ALL USING (
    current_setting('app.bypass_rls', true) = 'on'
    OR tenant_id = current_setting('app.tenant_id', true)
);
GRANT ALL ON TABLE customer_price_overrides TO velox_app;

-- ---------------------------------------------------------------------------
-- Credit Notes
-- Issued against an invoice to reduce the amount owed (partial or full).
-- Can result in a refund if the invoice was already paid.
-- ---------------------------------------------------------------------------
CREATE TABLE credit_notes (
    id                  TEXT PRIMARY KEY DEFAULT 'vlx_cn_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id           TEXT NOT NULL REFERENCES tenants(id),
    invoice_id          TEXT NOT NULL REFERENCES invoices(id),
    customer_id         TEXT NOT NULL REFERENCES customers(id),
    credit_note_number  TEXT NOT NULL,
    status              TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'issued', 'voided')),
    reason              TEXT NOT NULL,
    subtotal_cents      BIGINT NOT NULL DEFAULT 0,
    tax_amount_cents    BIGINT NOT NULL DEFAULT 0,
    total_cents         BIGINT NOT NULL DEFAULT 0,
    refund_amount_cents BIGINT NOT NULL DEFAULT 0,
    credit_amount_cents BIGINT NOT NULL DEFAULT 0,
    currency            TEXT NOT NULL DEFAULT 'USD',
    issued_at           TIMESTAMPTZ,
    voided_at           TIMESTAMPTZ,
    refund_status       TEXT DEFAULT 'none' CHECK (refund_status IN ('none', 'pending', 'succeeded', 'failed')),
    stripe_refund_id    TEXT,
    metadata            JSONB NOT NULL DEFAULT '{}',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, credit_note_number)
);

CREATE TABLE credit_note_line_items (
    id                  TEXT PRIMARY KEY DEFAULT 'vlx_cnli_' || encode(gen_random_bytes(12), 'hex'),
    credit_note_id      TEXT NOT NULL REFERENCES credit_notes(id),
    tenant_id           TEXT NOT NULL REFERENCES tenants(id),
    invoice_line_item_id TEXT,
    description         TEXT NOT NULL,
    quantity            BIGINT NOT NULL DEFAULT 0,
    unit_amount_cents   BIGINT NOT NULL DEFAULT 0,
    amount_cents        BIGINT NOT NULL DEFAULT 0,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE credit_notes ENABLE ROW LEVEL SECURITY;
ALTER TABLE credit_notes FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON credit_notes FOR ALL USING (
    current_setting('app.bypass_rls', true) = 'on'
    OR tenant_id = current_setting('app.tenant_id', true)
);

ALTER TABLE credit_note_line_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE credit_note_line_items FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON credit_note_line_items FOR ALL USING (
    current_setting('app.bypass_rls', true) = 'on'
    OR tenant_id = current_setting('app.tenant_id', true)
);

GRANT ALL ON TABLE credit_notes TO velox_app;
GRANT ALL ON TABLE credit_note_line_items TO velox_app;

-- ---------------------------------------------------------------------------
-- Prorated Invoicing Support
-- Track subscription changes mid-cycle for proration calculation.
-- ---------------------------------------------------------------------------
ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS previous_plan_id TEXT;
ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS plan_changed_at TIMESTAMPTZ;

ALTER TABLE invoice_line_items ADD COLUMN IF NOT EXISTS proration_factor NUMERIC(10,6);
