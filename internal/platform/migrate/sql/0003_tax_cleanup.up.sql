-- Remove deprecated float-based tax columns and redundant fields.
-- All tax rates now use basis points (integer) only.

-- tenant_settings: drop float tax_rate (replaced by tax_rate_bp)
ALTER TABLE tenant_settings DROP COLUMN IF EXISTS tax_rate;

-- customer_billing_profiles: drop redundant/deprecated columns
ALTER TABLE customer_billing_profiles DROP COLUMN IF EXISTS tax_identifier;
ALTER TABLE customer_billing_profiles DROP COLUMN IF EXISTS tax_country;
ALTER TABLE customer_billing_profiles DROP COLUMN IF EXISTS tax_state;
ALTER TABLE customer_billing_profiles DROP COLUMN IF EXISTS tax_override_rate;
ALTER TABLE customer_billing_profiles DROP COLUMN IF EXISTS tax_override_name;

-- invoices: drop float tax_rate (replaced by tax_rate_bp)
ALTER TABLE invoices DROP COLUMN IF EXISTS tax_rate;

-- invoice_line_items: drop float tax_rate (replaced by tax_rate_bp)
ALTER TABLE invoice_line_items DROP COLUMN IF EXISTS tax_rate;
