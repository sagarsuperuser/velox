-- 0136: prepaid commits + drawdown, phase 1 (ADR-078).
--
-- 1) grant_kind discriminates a purchased commit from a promotional grant
--    on the credit ledger. NULL = legacy/unclassified (proration credits,
--    CN grants, goodwill) and drains in the PAID class with commits —
--    they are money-derived liabilities, not marketing spend. Reuses
--    entry_type='grant' (no CHECK expansion; balance math is identical,
--    only drain order and reporting read the tag).
--
-- 2) source_invoice_id links a commit grant to its funding invoice. The
--    existing invoice_id column is NOT reusable: usage and reversal
--    entries stamp it, many rows per invoice. Partial-unique index =
--    structural fund-once idempotency (0093/0106 shape), predicated on
--    grant_kind='commit' so future non-commit writers that stamp
--    source_invoice_id can't collide with the commit slot.
--
-- 3) Commit line config on invoice_line_items: dedicated nullable
--    columns (the table's existing per-type pattern — meter_id,
--    pricing_mode, billing_period_*), NOT metadata JSONB (unvalidatable
--    money config). commit_granted_cents may differ from the line price
--    (discounted commits: pay $9k, get $10k). commit_expires_at NULL =
--    never expires (phase-1 default; term-aligned needs a term object —
--    phase 2). CHECK pins commit fields to positive amounts on add_on
--    lines only.
--
-- 4) Per-tenant low-balance alert threshold (credit.balance_low).
--    NULL = low alerts off; balance_depleted/recovered always fire.

ALTER TABLE customer_credit_ledger
    ADD COLUMN grant_kind TEXT
        CONSTRAINT credit_ledger_grant_kind_check
        CHECK (grant_kind IN ('commit', 'promotional'));

ALTER TABLE customer_credit_ledger
    ADD COLUMN source_invoice_id TEXT;

CREATE UNIQUE INDEX idx_credit_ledger_commit_fund_dedup
    ON customer_credit_ledger (tenant_id, source_invoice_id)
    WHERE source_invoice_id IS NOT NULL AND grant_kind = 'commit';

ALTER TABLE invoice_line_items
    ADD COLUMN commit_granted_cents BIGINT,
    ADD COLUMN commit_expires_at TIMESTAMPTZ;

ALTER TABLE invoice_line_items
    ADD CONSTRAINT invoice_line_items_commit_check
    CHECK (
        (commit_granted_cents IS NULL AND commit_expires_at IS NULL)
        OR (line_type = 'add_on' AND commit_granted_cents > 0)
    );

ALTER TABLE tenant_settings
    ADD COLUMN credit_balance_low_threshold_cents BIGINT;
