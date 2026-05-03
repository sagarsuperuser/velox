-- Per-mode invoice + credit-note sequence counters.
--
-- Until now tenant_settings carried a single invoice_next_seq (and
-- credit_note_next_seq) for the tenant — shared across test and live.
-- That gives test exploration the side effect of "burning" sequence
-- numbers that should belong to live invoices, leaving gaps like
-- INV-000001, ..., INV-000005, then live's first real invoice
-- becomes INV-000006 with no record-keeping reason. Stripe gives
-- each mode its own monotonic sequence; Velox now matches that.
--
-- Both new columns are seeded from the existing shared counter to
-- preserve the strict-monotonic guarantee (no number ever reused
-- per mode). Cosmetic gaps in test mode after the split are
-- intentional and acceptable — better than a re-issued number.
--
-- The UNIQUE constraint on (tenant_id, livemode, invoice_number)
-- already exists since 0020_test_mode, so test and live sequences
-- can independently start at any value with no clash risk.

ALTER TABLE tenant_settings
    ADD COLUMN invoice_next_seq_test     INT NOT NULL DEFAULT 1,
    ADD COLUMN invoice_next_seq_live     INT NOT NULL DEFAULT 1,
    ADD COLUMN credit_note_next_seq_test INT NOT NULL DEFAULT 1,
    ADD COLUMN credit_note_next_seq_live INT NOT NULL DEFAULT 1;

UPDATE tenant_settings
SET invoice_next_seq_test     = invoice_next_seq,
    invoice_next_seq_live     = invoice_next_seq,
    credit_note_next_seq_test = credit_note_next_seq,
    credit_note_next_seq_live = credit_note_next_seq;

ALTER TABLE tenant_settings
    DROP COLUMN invoice_next_seq,
    DROP COLUMN credit_note_next_seq;
