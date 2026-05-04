-- Background tax-retry reconciler (ADR-017).
--
-- Migration 0039 laid the groundwork for block-and-retry tax —
-- columns tax_status / tax_deferred_at / tax_retry_count /
-- tax_pending_reason exist, and the design comment promised
-- "a background worker retries." That worker was never wired;
-- a tax_status='pending' invoice sits forever waiting on a
-- manual operator click.
--
-- This migration adds the timing column the worker needs:
-- tax_next_retry_at. NULL means "ready to retry on the next
-- scheduler tick"; a future timestamp gates the row out of the
-- worker's scan until that time arrives. The reconciler
-- (Engine.RetryPendingTax) walks the partial index, recomputes
-- tax via the existing path, and either succeeds (auto-finalize
-- if billing_reason != 'manual'), retries with backoff, or
-- demotes to operator-action after the attempt cap.
--
-- Existing pending invoices get NULL — the reconciler picks
-- them up immediately. That's the right outcome: any invoice
-- already stuck in pending was waiting for a click that may
-- never have come.

ALTER TABLE invoices ADD COLUMN tax_next_retry_at TIMESTAMPTZ;

-- Partial index for the reconciler scan.
--
-- Predicate matches the worker query exactly so Postgres uses
-- this index instead of seq-scanning. The two-row-state filter
-- (status='draft' AND tax_status IN pending/failed) covers the
-- only invoices the worker can act on; tax_error_code is read
-- from the row, not the index, since adding it to the index
-- would re-bloat the partial set without narrowing the scan.
CREATE INDEX idx_invoices_tax_retry_due
    ON invoices (tenant_id, tax_next_retry_at NULLS FIRST)
    WHERE status = 'draft' AND tax_status IN ('pending', 'failed');
