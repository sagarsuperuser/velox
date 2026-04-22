-- Replace free-text company_address with structured fields to match the
-- billing_profile shape used on the customer side. Single consistent address
-- model across tenant and customer sides enables locale-aware PDF rendering,
-- reliable country-based tax rules, and zero drift between tax_home_country
-- and the printed "From" block.
--
-- Pre-launch clean drop: no backfill — operators will re-enter address on
-- first save. Acceptable per project-wide no-speculative-backfill policy.

ALTER TABLE tenant_settings
  ADD COLUMN company_address_line1 TEXT,
  ADD COLUMN company_address_line2 TEXT,
  ADD COLUMN company_city          TEXT,
  ADD COLUMN company_state         TEXT,
  ADD COLUMN company_postal_code   TEXT,
  ADD COLUMN company_country       TEXT,
  DROP COLUMN company_address;
