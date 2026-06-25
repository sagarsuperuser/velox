DROP INDEX IF EXISTS idx_credit_notes_tax_reversal_pending;
ALTER TABLE credit_notes DROP COLUMN IF EXISTS tax_reversal_pending;
