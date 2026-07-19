-- Rows in 'skipped' fold into 'failed' on downgrade (closest legacy
-- semantics: terminal, not delivered); the constraint then narrows back.
UPDATE email_outbox SET status = 'failed',
    last_error = COALESCE(NULLIF(last_error, ''), 'was skipped (obsolete action-required email); folded to failed on 0155 downgrade')
    WHERE status = 'skipped';
ALTER TABLE email_outbox DROP CONSTRAINT email_outbox_status_check;
ALTER TABLE email_outbox ADD CONSTRAINT email_outbox_status_check
    CHECK (status IN ('pending', 'dispatched', 'failed'));
