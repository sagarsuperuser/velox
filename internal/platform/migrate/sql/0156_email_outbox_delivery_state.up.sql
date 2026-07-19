-- 0156: email_outbox gains delivery_state — the PROVIDER-confirmed outcome
-- of a send, orthogonal to `status` (the SEND lifecycle). `status` answers
-- "did WE hand the message to the SMTP relay?" (pending/dispatched/failed/
-- skipped; single writer: the dispatcher's markCAS). delivery_state answers
-- "what did the provider learn about it afterwards?" (unknown/delivered/
-- bounced/complained; single writer: the Postmark webhook handler, ADR-098).
-- Two columns on purpose: overloading status would put two writers on one
-- column and lose the delivery-report-before-dispatched-mark race.
-- Severity-monotonic (unknown < delivered < bounced < complained): webhook
-- writes only promote, so at-least-once redelivery and Delivery-vs-Bounce
-- reordering converge to the most-severe truth without a dedup table.
ALTER TABLE email_outbox ADD COLUMN delivery_state TEXT NOT NULL DEFAULT 'unknown'
    CONSTRAINT email_outbox_delivery_state_check
    CHECK (delivery_state IN ('unknown', 'delivered', 'bounced', 'complained'));
