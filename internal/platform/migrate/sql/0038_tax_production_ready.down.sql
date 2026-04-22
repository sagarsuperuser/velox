DROP TABLE IF EXISTS tax_calculations;

ALTER TABLE plans DROP COLUMN IF EXISTS tax_code;

ALTER TABLE invoice_line_items
    DROP COLUMN IF EXISTS tax_jurisdiction,
    DROP COLUMN IF EXISTS tax_code;

ALTER TABLE invoices
    DROP COLUMN IF EXISTS tax_provider,
    DROP COLUMN IF EXISTS tax_calculation_id,
    DROP COLUMN IF EXISTS tax_reverse_charge,
    DROP COLUMN IF EXISTS tax_exempt_reason;

ALTER TABLE customer_billing_profiles
    DROP COLUMN IF EXISTS tax_status,
    DROP COLUMN IF EXISTS tax_exempt_reason,
    ADD COLUMN tax_exempt          BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN tax_override_rate_bp INTEGER;

ALTER TABLE tenant_settings
    DROP COLUMN IF EXISTS default_product_tax_code,
    ADD COLUMN tax_home_country TEXT NOT NULL DEFAULT '';
