-- 0155: email_outbox gains status='skipped' — an action-required email
-- (payment_setup_request / payment_failed / dunning_warning / dunning_
-- escalation) whose invoice settled while the row sat queued (SMTP down,
-- dispatcher paused) is OBSOLETE: delivering "action required — update
-- your payment method" AFTER the customer paid erodes trust. The
-- dispatcher skips such rows; 'skipped' records that the email was
-- deliberately not sent (≠ 'failed': nothing broke; ≠ 'dispatched': no
-- mail left). Found live in the 2026-07-19 FLOW E walkthrough: a
-- setup-request queued behind downed Mailpit delivered after credits
-- had settled the invoice.
ALTER TABLE email_outbox DROP CONSTRAINT email_outbox_status_check;
ALTER TABLE email_outbox ADD CONSTRAINT email_outbox_status_check
    CHECK (status IN ('pending', 'dispatched', 'failed', 'skipped'));
