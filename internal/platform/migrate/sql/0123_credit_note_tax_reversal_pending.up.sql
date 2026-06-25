-- Credit-note tax-reversal recovery marker.
--
-- Issue() reverses the invoice's upstream tax POST-COMMIT (an external Stripe
-- Tax call — never inside the issue transaction, per "no network I/O in a DB
-- tx"). Before PR2 that call was fire-and-forget: on failure it logged a WARN
-- and the credit note issued anyway, leaving the tenant's upstream tax reported
-- as still-collected → silent over-remit, with no automated recovery
-- (#310 RetryPendingTaxReversal scans status IN (voided,uncollectible) and keys
-- off invoices.tax_reversed_at, so a CN reversal on a finalized/paid invoice is
-- structurally invisible to it).
--
-- tax_reversal_pending is the FAST-PATH marker: set true when a reversal is
-- ATTEMPTED-AND-FAILED against a tax-bearing invoice, cleared on success. It is
-- an optimisation, NOT the sole recovery key — RetryPendingCreditNoteTaxReversal
-- ALSO derives eligibility structurally (an issued CN with no reversal stamped
-- against a tax-bearing stripe_tax source), so the recovery survives a failed
-- marker write (mirrors invoice #310, whose eligibility is the default persisted
-- state). The partial index below keeps the common fast-path scan cheap.
ALTER TABLE credit_notes
    ADD COLUMN tax_reversal_pending BOOLEAN NOT NULL DEFAULT false;

-- Partial index: only the handful of pending rows are indexed, so the
-- cross-tenant sweep is a cheap index scan and never walks the full table.
CREATE INDEX idx_credit_notes_tax_reversal_pending
    ON credit_notes (updated_at)
    WHERE tax_reversal_pending;
