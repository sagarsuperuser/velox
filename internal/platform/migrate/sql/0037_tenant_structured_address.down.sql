ALTER TABLE tenant_settings
  ADD COLUMN company_address TEXT,
  DROP COLUMN company_country,
  DROP COLUMN company_postal_code,
  DROP COLUMN company_state,
  DROP COLUMN company_city,
  DROP COLUMN company_address_line2,
  DROP COLUMN company_address_line1;
