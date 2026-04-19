-- Clear any 'unknown' rows so the old CHECK reinstalls cleanly. In practice
-- this downgrade would only run in test/dev; 'unknown' invoices in prod
-- should be reconciled first.
UPDATE invoices SET payment_status = 'failed' WHERE payment_status = 'unknown';

ALTER TABLE invoices
    DROP CONSTRAINT invoices_payment_status_check;

ALTER TABLE invoices
    ADD CONSTRAINT invoices_payment_status_check
    CHECK (payment_status IN ('pending', 'processing', 'succeeded', 'failed'));
