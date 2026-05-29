DROP INDEX IF EXISTS idx_credit_ledger_credit_note_dedup;

ALTER TABLE customer_credit_ledger
    DROP COLUMN IF EXISTS source_credit_note_id;
