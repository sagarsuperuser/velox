-- Tax rate stored as percentage (e.g. 18.00 = 18%, 7.25 = 7.25%)
ALTER TABLE tenant_settings ADD COLUMN IF NOT EXISTS tax_rate NUMERIC(6,2) NOT NULL DEFAULT 0;
ALTER TABLE tenant_settings ADD COLUMN IF NOT EXISTS tax_name TEXT NOT NULL DEFAULT '';

-- Snapshot tax rate + name on each invoice for historical accuracy
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS tax_rate NUMERIC(6,2) NOT NULL DEFAULT 0;
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS tax_name TEXT NOT NULL DEFAULT '';

-- Explicit tax-exempt flag per customer
ALTER TABLE customer_billing_profiles ADD COLUMN IF NOT EXISTS tax_exempt BOOLEAN NOT NULL DEFAULT false;
