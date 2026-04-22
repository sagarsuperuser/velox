-- Block-and-retry tax calculation.
--
-- When a Stripe Tax call fails transiently (API outage, timeout), the prior
-- design silently fell back to the tenant-configured manual rate. That trades
-- correctness for availability — a flat rate is almost always wrong in
-- jurisdictions where the tenant picked Stripe Tax precisely for accuracy.
-- The new default defers the invoice: tax stays unset, the invoice stays in
-- draft with tax_status='pending', and a background worker retries.
--
-- Tenants who explicitly want the old availability-over-accuracy trade-off
-- opt into tax_on_failure='fallback_manual'. Existing tenants are migrated
-- to that value to preserve current behaviour — no silent policy flip.
--
-- Columns:
--   invoices.tax_status          — 'ok' | 'pending' | 'failed'
--   invoices.tax_deferred_at     — first time calculation failed
--   invoices.tax_retry_count     — attempts made so far
--   invoices.tax_pending_reason  — last provider error (truncated, audit in tax_calculations)
--   tenant_settings.tax_on_failure — 'block' | 'fallback_manual'
--
-- ok is the happy-path terminal: calculation succeeded (including zero-tax
-- outcomes from none/manual/exempt/reverse-charge — those aren't failures).
-- pending is the in-flight state awaiting retry. failed is the giveup
-- terminal after sustained failures; operators resolve manually.

ALTER TABLE invoices
    ADD COLUMN tax_status         TEXT        NOT NULL DEFAULT 'ok'
        CHECK (tax_status IN ('ok', 'pending', 'failed')),
    ADD COLUMN tax_deferred_at    TIMESTAMPTZ,
    ADD COLUMN tax_retry_count    INTEGER     NOT NULL DEFAULT 0,
    ADD COLUMN tax_pending_reason TEXT        NOT NULL DEFAULT '';

-- Partial index: the retry worker queries only pending/failed rows. Most
-- invoices are 'ok' forever, so a partial index keeps the footprint small.
CREATE INDEX idx_invoices_tax_status_pending
    ON invoices (tenant_id, tax_deferred_at)
    WHERE tax_status IN ('pending', 'failed');

ALTER TABLE tenant_settings
    ADD COLUMN tax_on_failure TEXT NOT NULL DEFAULT 'block'
        CHECK (tax_on_failure IN ('block', 'fallback_manual'));

-- Preserve current behaviour for tenants who already have settings rows.
-- New default is 'block'; existing tenants keep 'fallback_manual' until they
-- explicitly opt into the safer default via the settings UI.
UPDATE tenant_settings SET tax_on_failure = 'fallback_manual';
