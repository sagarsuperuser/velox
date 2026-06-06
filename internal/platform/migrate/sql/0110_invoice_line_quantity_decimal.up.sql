-- Exact (possibly fractional) usage quantities on invoice lines.
--
-- The integer `quantity` column truncated fractional usage (e.g. 1.5 GPU-hours
-- stored as 1), so a line's quantity × unit_amount no longer reconciled to its
-- amount_cents. The charge (amount_cents) was always correct; only the line's
-- displayed quantity was wrong. Industry parity: Stripe `quantity_decimal`,
-- Chargebee `quantity_in_decimal` — a decimal quantity companion, with the
-- integer `quantity` retained (truncated) for back-compat and the line amount
-- still in whole cents.
--
-- 0 means "no decimal quantity — use the integer quantity" (non-usage lines:
-- base fees, proration, manual). NOT NULL DEFAULT 0 is a metadata-only add on
-- Postgres 11+ (no table rewrite).
ALTER TABLE invoice_line_items
    ADD COLUMN quantity_decimal NUMERIC(38,12) NOT NULL DEFAULT 0;
