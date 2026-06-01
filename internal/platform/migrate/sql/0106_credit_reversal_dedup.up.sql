-- 0106: idempotent invoice-void credit reversals.
--
-- credit.ReverseForInvoice (invoice void / dunning manual-resolve) summed the
-- invoice's applied-credit `usage` entries and appended a reversal `grant`
-- with no dedup. Voiding an invoice and then manual-resolving the same
-- invoice's dunning run (or any retry of the void) re-summed the untouched
-- usage rows and re-granted the full amount — double-crediting the customer's
-- balance. Stamp each reversal grant with the source invoice and enforce
-- one-reversal-per-invoice with a partial unique index, so a second reversal
-- hits the constraint and no-ops instead of double-crediting.
ALTER TABLE customer_credit_ledger ADD COLUMN source_invoice_reversal_id text;

CREATE UNIQUE INDEX idx_credit_ledger_reversal_dedup
    ON customer_credit_ledger (tenant_id, source_invoice_reversal_id)
    WHERE source_invoice_reversal_id IS NOT NULL;
