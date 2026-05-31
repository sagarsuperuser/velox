-- Reverse migration 0104 — drops the tax_rate NUMERIC columns. The
-- tax_rate_bp columns remain populated (they were never dropped on the
-- forward migration), so post-revert state matches pre-migration state
-- for any reader still using tax_rate_bp.

ALTER TABLE invoices            DROP COLUMN IF EXISTS tax_rate;
ALTER TABLE invoice_line_items  DROP COLUMN IF EXISTS tax_rate;
ALTER TABLE tenant_settings     DROP COLUMN IF EXISTS tax_rate;
