ALTER TABLE credit_notes DROP COLUMN IF EXISTS commit_retired_cents;
ALTER TABLE customer_credit_ledger DROP COLUMN IF EXISTS cn_retired_cents;
