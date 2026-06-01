DROP INDEX IF EXISTS idx_credit_ledger_reversal_dedup;
ALTER TABLE customer_credit_ledger DROP COLUMN IF EXISTS source_invoice_reversal_id;
