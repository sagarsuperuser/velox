-- Reverse migration 0105 — re-add tax_rate_bp columns, backfill from
-- tax_rate (banker's rounded to whole bp, losing precision below
-- 0.01%). Post-revert state matches the dual-column shape from
-- migration 0104.

ALTER TABLE invoices            ADD COLUMN tax_rate_bp BIGINT NOT NULL DEFAULT 0;
ALTER TABLE invoice_line_items  ADD COLUMN tax_rate_bp BIGINT NOT NULL DEFAULT 0;
ALTER TABLE tenant_settings     ADD COLUMN tax_rate_bp BIGINT NOT NULL DEFAULT 0;

UPDATE invoices            SET tax_rate_bp = ROUND(tax_rate * 100)::bigint;
UPDATE invoice_line_items  SET tax_rate_bp = ROUND(tax_rate * 100)::bigint;
UPDATE tenant_settings     SET tax_rate_bp = ROUND(tax_rate * 100)::bigint;
