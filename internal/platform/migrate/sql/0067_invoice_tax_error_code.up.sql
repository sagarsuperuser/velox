-- Tax error classification on deferred invoices. Today the engine
-- captures the upstream provider's full error payload as
-- tax_pending_reason (free-form text) — readable for diagnosis but
-- unsuited to programmatic routing. tax_error_code adds a typed
-- taxonomy alongside, so the operator UX can branch on cause
-- (customer-data fix vs provider-outage retry) and webhook consumers
-- can route alerts.
--
-- Five values, mirroring the Chargebee taxonomy our research
-- surfaced as the cleanest precedent:
--   customer_data_invalid    — missing/malformed postal_code, country, tax_id
--   jurisdiction_unsupported — provider can't compute for the customer's region
--   provider_outage          — 5xx, network, timeout
--   provider_auth            — invalid/revoked Stripe key
--   unknown                  — fallback when classification can't be inferred
--
-- Soft taxonomy: stored as TEXT with a CHECK constraint so we can add
-- new codes without an ALTER (Stripe Tax surfaces new error_codes
-- periodically and we want graceful degradation, not a deploy block).
-- Existing rows leave the column NULL — operators see the raw
-- tax_pending_reason as before. Going forward, new defers populate
-- both fields.

ALTER TABLE invoices ADD COLUMN tax_error_code TEXT
    CHECK (tax_error_code IS NULL OR tax_error_code IN (
        'customer_data_invalid',
        'jurisdiction_unsupported',
        'provider_outage',
        'provider_auth',
        'unknown'
    ));

-- Partial index on the deferred-invoice path (tax_status='pending'
-- with a code set). Powers the Invoices list filter chip ("Needs
-- attention") and the planned operator-queue count badge.
CREATE INDEX idx_invoices_tax_pending
    ON invoices (tenant_id, tax_status, tax_error_code)
    WHERE tax_status IN ('pending', 'failed');
