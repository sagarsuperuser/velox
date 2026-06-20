DROP INDEX IF EXISTS idx_credit_notes_issue_pending;
ALTER TABLE credit_notes DROP COLUMN IF EXISTS issue_pending;
