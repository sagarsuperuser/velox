-- Track credits applied to invoices and amount actually paid
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS credits_applied_cents BIGINT NOT NULL DEFAULT 0;

-- Backfill paid invoices: amount_paid = total - credit_notes_on_that_invoice
UPDATE invoices
SET amount_paid_cents = total_amount_cents - COALESCE(
    (SELECT SUM(cn.total_cents) FROM credit_notes cn WHERE cn.invoice_id = invoices.id), 0
)
WHERE status = 'paid' AND amount_paid_cents = 0;

-- Credit note sequential numbering (collision-safe, like invoice numbering)
ALTER TABLE tenant_settings ADD COLUMN IF NOT EXISTS credit_note_prefix TEXT NOT NULL DEFAULT 'CN';
ALTER TABLE tenant_settings ADD COLUMN IF NOT EXISTS credit_note_next_seq INT NOT NULL DEFAULT 1;

-- Backfill: set credit_note_next_seq to max existing CN count + 1 per tenant
UPDATE tenant_settings ts
SET credit_note_next_seq = COALESCE(
    (SELECT COUNT(*) + 1 FROM credit_notes cn WHERE cn.tenant_id = ts.tenant_id), 1
);
