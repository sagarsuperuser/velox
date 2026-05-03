-- Reverse the per-mode sequence split. The merged value picks the
-- larger of the two per-mode counters so neither sequence regresses
-- across the down + up cycle (a number once handed out is never
-- handed out again, regardless of which mode allocated it).

ALTER TABLE tenant_settings
    ADD COLUMN invoice_next_seq     INT NOT NULL DEFAULT 1,
    ADD COLUMN credit_note_next_seq INT NOT NULL DEFAULT 1;

UPDATE tenant_settings
SET invoice_next_seq     = GREATEST(invoice_next_seq_test, invoice_next_seq_live),
    credit_note_next_seq = GREATEST(credit_note_next_seq_test, credit_note_next_seq_live);

ALTER TABLE tenant_settings
    DROP COLUMN invoice_next_seq_test,
    DROP COLUMN invoice_next_seq_live,
    DROP COLUMN credit_note_next_seq_test,
    DROP COLUMN credit_note_next_seq_live;
