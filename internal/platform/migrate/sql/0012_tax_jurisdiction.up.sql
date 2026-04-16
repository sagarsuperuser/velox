-- Per-customer tax jurisdiction support
ALTER TABLE customer_billing_profiles ADD COLUMN IF NOT EXISTS tax_id TEXT NOT NULL DEFAULT '';
ALTER TABLE customer_billing_profiles ADD COLUMN IF NOT EXISTS tax_id_type TEXT NOT NULL DEFAULT '';
ALTER TABLE customer_billing_profiles ADD COLUMN IF NOT EXISTS tax_country TEXT NOT NULL DEFAULT '';
ALTER TABLE customer_billing_profiles ADD COLUMN IF NOT EXISTS tax_state TEXT NOT NULL DEFAULT '';
ALTER TABLE customer_billing_profiles ADD COLUMN IF NOT EXISTS tax_override_rate NUMERIC(6,2);
ALTER TABLE customer_billing_profiles ADD COLUMN IF NOT EXISTS tax_override_name TEXT NOT NULL DEFAULT '';

-- Store resolved tax jurisdiction on invoices for historical accuracy
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS tax_country TEXT NOT NULL DEFAULT '';
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS tax_id TEXT NOT NULL DEFAULT '';
