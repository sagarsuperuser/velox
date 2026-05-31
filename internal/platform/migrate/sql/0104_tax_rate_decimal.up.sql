-- Migration 0104: tax rate precision upgrade (B7.3 / ADR-042).
--
-- Velox stores tax rates as integer basis points (tax_rate_bp) — 1 bp =
-- 0.01% precision. The 2026-05-31 audit found this lossy for real
-- jurisdictions: NYC 8.875% rounds to 888 bp (8.88%); Quebec QST 9.975%
-- rounds to 997 bp (9.97%); Hawaii GET 4.7120% rounds to 471 bp (4.71%);
-- Tennessee local rates at 0.25% precision are unrepresentable.
--
-- Industry peers (Stripe Tax, Lago, Chargebee, Recurly) all use decimal
-- precision (3-4 decimal places minimum). Stripe Tax explicitly returns
-- `percentage_decimal` as a STRING to avoid lossy float round-trip.
--
-- This migration adds tax_rate NUMERIC(7,4) columns on the three tables
-- that store rates and backfills from tax_rate_bp / 100. The tax_rate_bp
-- columns are RETAINED for backward compat during transition — code
-- will write both during the transition window. A future migration drops
-- tax_rate_bp once all readers are confirmed switched over.
--
-- NUMERIC(7,4) chosen to match Stripe Tax's 4-decimal precision:
-- - 7 total digits, 4 after decimal → max representable: 999.9999%
-- - Sufficient for every real tax rate (max ~99% in some compound cases)
-- - Matches percentage_decimal string range from Stripe Tax API

ALTER TABLE invoices
    ADD COLUMN tax_rate NUMERIC(7,4) NOT NULL DEFAULT 0;

ALTER TABLE invoice_line_items
    ADD COLUMN tax_rate NUMERIC(7,4) NOT NULL DEFAULT 0;

ALTER TABLE tenant_settings
    ADD COLUMN tax_rate NUMERIC(7,4) NOT NULL DEFAULT 0;

-- Backfill: tax_rate_bp / 100 = percent value. 725 bp → 7.25%.
UPDATE invoices            SET tax_rate = tax_rate_bp::numeric / 100;
UPDATE invoice_line_items  SET tax_rate = tax_rate_bp::numeric / 100;
UPDATE tenant_settings     SET tax_rate = tax_rate_bp::numeric / 100;
