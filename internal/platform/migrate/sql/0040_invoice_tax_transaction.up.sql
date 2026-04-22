-- Persist the committed Stripe Tax transaction_id on the invoice.
--
-- Stripe's tax_calculation is an ephemeral quote (24 h TTL); commit_tax
-- promotes it to a tax_transaction (tx_xxx), which is the durable record
-- Stripe Tax reporting, filings, and reversals key off. Without storing
-- this id we cannot later call tax.transactions.createReversal when a
-- credit note is issued against the invoice — the reversal API requires
-- the original transaction id, not the calculation id.
--
-- Nullable because:
--  - Providers with no upstream state (none, manual) return an empty id.
--  - Legacy invoices finalized before this column existed have none.
--  - Invoices in tax_status='pending' or 'failed' have no transaction yet.

ALTER TABLE invoices
    ADD COLUMN tax_transaction_id TEXT;
