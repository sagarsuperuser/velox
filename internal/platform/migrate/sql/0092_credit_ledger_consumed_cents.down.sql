DROP INDEX IF EXISTS idx_credit_ledger_active_grants;

ALTER TABLE customer_credit_ledger
    DROP CONSTRAINT IF EXISTS customer_credit_ledger_consumed_cents_check;

ALTER TABLE customer_credit_ledger
    DROP COLUMN IF EXISTS consumed_cents;
