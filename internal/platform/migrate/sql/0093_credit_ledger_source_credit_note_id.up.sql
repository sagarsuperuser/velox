-- Bug B fix: dedup credit grants created by credit-note Issue() so a
-- retry after partial-failure doesn't create a duplicate grant.
--
-- Pre-fix shape: creditnote.Issue() calls credit.Service.Grant() to
-- restore the credit-paid portion of a refund. If a downstream step
-- (tax reversal / coupon void / status update) fails, the function
-- returns error with cn.status still='draft'. Operator retries Issue()
-- → second Grant() call appends a SECOND grant row → customer
-- over-credited.
--
-- Fix: per-CN dedup column. A retry attempt re-tries the same
-- (tenant_id, source_credit_note_id) pair, hits the unique index,
-- store returns ErrAlreadyExists, service fetches the existing grant
-- and continues. Mirrors the existing idx_credit_ledger_proration_dedup
-- pattern for subscription-source grants.

ALTER TABLE customer_credit_ledger
    ADD COLUMN source_credit_note_id TEXT;

-- Partial unique index: only constrains rows where the source CN id
-- is set (proration grants and operator-driven grants leave it NULL).
-- One grant per (tenant, CN) is the invariant.
CREATE UNIQUE INDEX IF NOT EXISTS idx_credit_ledger_credit_note_dedup
    ON customer_credit_ledger (tenant_id, source_credit_note_id)
    WHERE source_credit_note_id IS NOT NULL;
