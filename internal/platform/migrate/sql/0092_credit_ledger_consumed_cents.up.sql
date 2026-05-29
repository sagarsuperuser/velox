-- Adopt Orb's credit-block model on customer_credit_ledger: each grant
-- tracks how much of its original amount has been drained by usage
-- entries. Expiry deducts only the remaining (un-drained) portion, not
-- the full original grant amount.
--
-- Without this: expiring an already-consumed grant double-counts the
-- deduction (the usage entry already debited the balance once;
-- expiring the full grant amount debits it again), driving the
-- customer's balance arbitrarily negative.
--
-- Industry shape (verified 2026-05-22): Orb's credit-block model uses
-- per-block "remaining" with FIFO drainage by soonest-expiring then
-- earliest-created. Velox adopts the same pattern via a single
-- consumed_cents column on grant rows.

ALTER TABLE customer_credit_ledger
    ADD COLUMN consumed_cents BIGINT NOT NULL DEFAULT 0;

-- consumed_cents is meaningful only for entry_type='grant'. For other
-- entry types (usage, expiry, adjustment) it stays at 0. The CHECK
-- enforces: never below 0, never exceeds the grant's amount.
ALTER TABLE customer_credit_ledger
    ADD CONSTRAINT customer_credit_ledger_consumed_cents_check
        CHECK (
            consumed_cents >= 0
            AND (entry_type <> 'grant' OR consumed_cents <= amount_cents)
        );

-- Index for the FIFO-drainage scan in ApplyToInvoiceAtomic: lock
-- active (un-fully-consumed) grant rows for a customer ordered by
-- (expires_at NULLS LAST, created_at). Partial index narrows to
-- the active-grants subset that the drainage path scans.
CREATE INDEX IF NOT EXISTS idx_credit_ledger_active_grants
    ON customer_credit_ledger (tenant_id, customer_id, expires_at NULLS LAST, created_at)
    WHERE entry_type = 'grant' AND consumed_cents < amount_cents;
