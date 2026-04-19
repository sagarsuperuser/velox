-- Widen tax_rate_bp from INT (int4) to BIGINT (int8).
--
-- Functional context: migration 0003 already dropped the legacy
-- NUMERIC(6,2) tax_rate columns in favour of integer basis points, so
-- tax arithmetic is fully deterministic. INT4 (max 2,147,483,647) can
-- technically store 21 million percent, which is not an overflow risk
-- in practice — but every other money-adjacent column in the schema
-- (tax_amount_cents, subtotal_cents, total_amount_cents, …) is BIGINT.
-- A lone INT here invites "why is this one different?" confusion on
-- the next person to read the schema and is exactly the kind of
-- gratuitous inconsistency the HYG- cleanup items target.
--
-- ALTER … TYPE BIGINT is a cheap in-place metadata change on
-- Postgres 12+ when the source type is INT (no rewrite required), so
-- this is safe regardless of table size.
ALTER TABLE tenant_settings ALTER COLUMN tax_rate_bp TYPE BIGINT;
ALTER TABLE invoices ALTER COLUMN tax_rate_bp TYPE BIGINT;
ALTER TABLE invoice_line_items ALTER COLUMN tax_rate_bp TYPE BIGINT;
