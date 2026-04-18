-- Restore deprecated float-based tax columns.

ALTER TABLE tenant_settings ADD COLUMN IF NOT EXISTS tax_rate NUMERIC(6,2) DEFAULT 0;
ALTER TABLE customer_billing_profiles ADD COLUMN IF NOT EXISTS tax_identifier TEXT;
ALTER TABLE customer_billing_profiles ADD COLUMN IF NOT EXISTS tax_country TEXT NOT NULL DEFAULT '';
ALTER TABLE customer_billing_profiles ADD COLUMN IF NOT EXISTS tax_state TEXT NOT NULL DEFAULT '';
ALTER TABLE customer_billing_profiles ADD COLUMN IF NOT EXISTS tax_override_rate NUMERIC(6,2);
ALTER TABLE customer_billing_profiles ADD COLUMN IF NOT EXISTS tax_override_name TEXT NOT NULL DEFAULT '';
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS tax_rate NUMERIC(6,2) DEFAULT 0;
ALTER TABLE invoice_line_items ADD COLUMN IF NOT EXISTS tax_rate NUMERIC(5,4) DEFAULT 0;
