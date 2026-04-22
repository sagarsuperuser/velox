-- Persist the upstream tax_transaction id returned by a reversal call.
--
-- When a credit note is issued against a taxable invoice, the tax flow
-- calls tax.transactions.createReversal on Stripe (or the equivalent on a
-- domestic-GST provider). The returned tx_xxx is the negative tax
-- transaction that appears in Stripe Tax reports and on the filed VAT/GST
-- return. Storing it locally lets us:
--
--   1. Show operators which reversal corresponds to which credit note
--      without cross-referencing Stripe's dashboard.
--   2. Detect a retried Issue and skip the reversal call (the presence of
--      a non-empty value means the reversal already succeeded).
--   3. Audit: match a Stripe Tax filing row back to the internal
--      credit_note id that triggered it.
--
-- Nullable because:
--   - Draft credit notes have not issued yet.
--   - Credit notes against invoices with no upstream state (none/manual
--     provider, or legacy invoices pre-0040) have nothing to reverse.
--   - A reversal call failure persists the CN issued without this id,
--     and an operator retries through the admin flow.

ALTER TABLE credit_notes
    ADD COLUMN tax_transaction_id TEXT;
