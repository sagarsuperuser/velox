-- Production-grade tax schema. Consolidates the final shape after the
-- redesign around Stripe Tax as a first-class provider. See the design doc
-- commit message for the full rationale; highlights:
--
--  * Replace tax_exempt boolean on customer_billing_profiles with a
--    three-value tax_status enum ({standard, exempt, reverse_charge}) plus
--    a free-text tax_exempt_reason. Stripe Tax distinguishes "exempt" from
--    "reverse charge" and the invoice PDF legends diverge.
--  * Drop tax_override_rate_bp. Per-customer rate overrides are a
--    compliance foot-gun with no legitimate use case — customers that need
--    a different rate are either exempt or under reverse charge.
--  * Drop tax_home_country. Redundant with company_country (structured
--    address). The former's only real use was automatic cross-border
--    zero-rating, which is wrong in manual mode (would incorrectly zero
--    out B2C exports under OSS/OIDAR) and handled natively by Stripe Tax.
--  * Add default_product_tax_code to tenant_settings: the Stripe Tax
--    product classification applied when a plan doesn't specify its own
--    (txcd_10103001 = SaaS business-use by default).
--  * Add per-invoice tax audit snapshot: tax_provider, tax_calculation_id,
--    tax_reverse_charge, tax_exempt_reason. Stripe Tax calculations expire
--    after 90 days — we need durable retention for post-hoc audit.
--  * Add per-line tax_jurisdiction + tax_code: enables multi-jurisdiction
--    breakdown tables on invoices (EU cross-state, India CGST+SGST, US
--    state + local) instead of a single aggregate tax line.
--  * New tax_calculations table: durable JSONB request/response snapshot
--    for every provider calculation, regardless of provider.
--  * Add tax_code to plans so per-product classification can override the
--    tenant default when needed.
--
-- Pre-launch: no backfill for tax_exempt → tax_status conversion. Operators
-- re-set customer tax status on next edit per project-wide
-- no-speculative-backfill policy. Same pattern as migration 0037.

-- ---------------------------------------------------------------------------
-- tenant_settings
-- ---------------------------------------------------------------------------
ALTER TABLE tenant_settings
    DROP COLUMN tax_home_country,
    ADD COLUMN default_product_tax_code TEXT NOT NULL DEFAULT 'txcd_10103001';

-- ---------------------------------------------------------------------------
-- customer_billing_profiles: exempt bool → tax_status enum + reason
-- ---------------------------------------------------------------------------
ALTER TABLE customer_billing_profiles
    DROP COLUMN tax_exempt,
    DROP COLUMN tax_override_rate_bp,
    ADD COLUMN tax_status TEXT NOT NULL DEFAULT 'standard'
        CHECK (tax_status IN ('standard', 'exempt', 'reverse_charge')),
    ADD COLUMN tax_exempt_reason TEXT NOT NULL DEFAULT '';

-- ---------------------------------------------------------------------------
-- invoices: durable audit snapshot
-- ---------------------------------------------------------------------------
ALTER TABLE invoices
    ADD COLUMN tax_provider       TEXT    NOT NULL DEFAULT '',
    ADD COLUMN tax_calculation_id TEXT    NOT NULL DEFAULT '',
    ADD COLUMN tax_reverse_charge BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN tax_exempt_reason  TEXT    NOT NULL DEFAULT '';

-- ---------------------------------------------------------------------------
-- invoice_line_items: per-line jurisdiction + tax_code
-- ---------------------------------------------------------------------------
ALTER TABLE invoice_line_items
    ADD COLUMN tax_jurisdiction TEXT NOT NULL DEFAULT '',
    ADD COLUMN tax_code         TEXT NOT NULL DEFAULT '';

-- ---------------------------------------------------------------------------
-- plans: optional per-product Stripe Tax code
-- ---------------------------------------------------------------------------
ALTER TABLE plans
    ADD COLUMN tax_code TEXT NOT NULL DEFAULT '';

-- ---------------------------------------------------------------------------
-- tax_calculations: durable request/response audit trail
-- ---------------------------------------------------------------------------
CREATE TABLE tax_calculations (
    id            TEXT PRIMARY KEY DEFAULT 'vlx_tcalc_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id     TEXT NOT NULL REFERENCES tenants(id),
    invoice_id    TEXT REFERENCES invoices(id),
    provider      TEXT NOT NULL CHECK (provider IN ('none', 'manual', 'stripe_tax')),
    provider_ref  TEXT NOT NULL DEFAULT '',
    request       JSONB NOT NULL,
    response      JSONB NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_tax_calculations_tenant
    ON tax_calculations (tenant_id, created_at DESC);

CREATE INDEX idx_tax_calculations_invoice
    ON tax_calculations (invoice_id)
    WHERE invoice_id IS NOT NULL;

ALTER TABLE tax_calculations ENABLE ROW LEVEL SECURITY;
ALTER TABLE tax_calculations FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON tax_calculations FOR ALL USING (
    current_setting('app.bypass_rls', true) = 'on'
    OR tenant_id = current_setting('app.tenant_id', true)
);
