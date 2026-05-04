-- Capture the card brand + last4 used to settle each invoice
-- (ADR-020). Surfaces in the invoice activity timeline as a
-- sub-line on the "Invoice paid" row, e.g.
--
--     Invoice paid · $29.00
--     via Visa •••• 4242
--
-- Without this, an operator scanning the timeline sees the
-- amount but not which payment instrument was charged. Stripe
-- Dashboard / Lago / Recurly all show this inline; Velox now
-- matches.
--
-- Populated at payment_intent.succeeded webhook handling time
-- by looking up the PI's payment_method against our existing
-- payment_methods table (which is upserted on setup_intent /
-- checkout flows). Falls back to empty strings when the PM is
-- unknown to us — e.g. one-off Checkout cards never saved as
-- a customer-attached PM. Empty fields render no sub-line;
-- graceful degradation.
--
-- Both columns nullable because (a) historical paid invoices
-- predate this migration and (b) the lookup may legitimately
-- return nothing on the one-off case above.

ALTER TABLE invoices
    ADD COLUMN payment_card_brand TEXT,
    ADD COLUMN payment_card_last4 TEXT;
