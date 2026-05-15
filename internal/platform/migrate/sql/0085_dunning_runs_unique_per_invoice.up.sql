-- One dunning run per (tenant_id, invoice_id), lifetime.
--
-- The schema previously allowed multiple runs per invoice — a re-
-- triggered StartDunning (e.g. operator-initiated retry, auto-charge
-- retry after PM update) would call CreateRun whenever the existing
-- run wasn't ACTIVE (it filtered state NOT IN ('resolved','escalated')).
-- Escalated runs are terminal in our dunning state machine, so any
-- subsequent run for the same invoice is a duplicate, not a fresh
-- campaign. Stripe parity: subscriptions transition to past_due/unpaid
-- and stay there until operator intervention; no automatic second
-- dunning campaign.
--
-- This migration:
--   1. Drops dunning_events for any duplicate (non-oldest) runs.
--      RESTRICT FK forbids deleting a run with events, so events go
--      first.
--   2. Drops the duplicate runs themselves — keep the oldest run per
--      (tenant_id, invoice_id) because it carries the original
--      retry-attempt history.
--   3. Adds a UNIQUE index enforcing the invariant going forward.

-- Step 1: delete events tied to duplicate runs.
DELETE FROM invoice_dunning_events
WHERE run_id IN (
  SELECT id FROM invoice_dunning_runs r
  WHERE EXISTS (
    SELECT 1 FROM invoice_dunning_runs r2
    WHERE r2.tenant_id = r.tenant_id
      AND r2.invoice_id = r.invoice_id
      AND r2.id <> r.id
      AND r2.created_at < r.created_at
  )
);

-- Step 2: delete the duplicate runs (keep oldest per invoice).
DELETE FROM invoice_dunning_runs r
WHERE EXISTS (
  SELECT 1 FROM invoice_dunning_runs r2
  WHERE r2.tenant_id = r.tenant_id
    AND r2.invoice_id = r.invoice_id
    AND r2.id <> r.id
    AND r2.created_at < r.created_at
);

-- Step 3: prevent recurrence.
CREATE UNIQUE INDEX idx_dunning_runs_one_per_invoice
  ON invoice_dunning_runs (tenant_id, invoice_id);
