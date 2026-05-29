-- Restore the email column. Re-encrypted-at-rest like all other PII
-- columns. Note: rolling back leaves the column empty for any rows
-- created post-0100; the operator would need to repopulate via
-- billing-profile upsert if the column is genuinely needed again
-- (which the e2e audit concluded it isn't).

ALTER TABLE customer_billing_profiles ADD COLUMN IF NOT EXISTS email TEXT;
