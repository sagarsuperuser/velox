ALTER TABLE tenant_settings
    DROP COLUMN IF EXISTS credit_balance_low_threshold_cents;

ALTER TABLE invoice_line_items
    DROP CONSTRAINT IF EXISTS invoice_line_items_commit_check;

ALTER TABLE invoice_line_items
    DROP COLUMN IF EXISTS commit_granted_cents,
    DROP COLUMN IF EXISTS commit_expires_at;

DROP INDEX IF EXISTS idx_credit_ledger_commit_fund_dedup;

ALTER TABLE customer_credit_ledger
    DROP COLUMN IF EXISTS source_invoice_id,
    DROP COLUMN IF EXISTS grant_kind;
