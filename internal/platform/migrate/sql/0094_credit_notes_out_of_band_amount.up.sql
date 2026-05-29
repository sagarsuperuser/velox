-- Adds the third explicit-allocation channel to credit notes, matching
-- Stripe (`out_of_band_amount`) and Lago (`offset_amount_cents`). With
-- refund_amount_cents + credit_amount_cents already on the row, this
-- completes the three-bucket allocation: refund to PM, credit to balance,
-- handled outside Stripe (cash, ACH, manual reversal).

ALTER TABLE credit_notes
    ADD COLUMN out_of_band_amount_cents BIGINT NOT NULL DEFAULT 0
        CHECK (out_of_band_amount_cents >= 0);
