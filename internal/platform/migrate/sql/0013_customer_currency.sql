ALTER TABLE customer_billing_profiles ADD COLUMN IF NOT EXISTS currency TEXT NOT NULL DEFAULT '';
