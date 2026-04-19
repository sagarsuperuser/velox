-- Introduce the 'unknown' payment status. Set when a Stripe PaymentIntent
-- attempt fails with an ambiguous error (5xx, timeout, connection reset)
-- where Stripe may or may not have processed the charge server-side.
-- A reconciler worker resolves unknown → succeeded/failed by querying
-- Stripe after a cool-off period.
ALTER TABLE invoices
    DROP CONSTRAINT invoices_payment_status_check;

ALTER TABLE invoices
    ADD CONSTRAINT invoices_payment_status_check
    CHECK (payment_status IN ('pending', 'processing', 'succeeded', 'failed', 'unknown'));
