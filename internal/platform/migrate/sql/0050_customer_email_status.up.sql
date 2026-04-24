-- Customer email deliverability tracking (T0-20).
--
-- MVP for bounce handling. email_status holds the last known outcome of
-- delivery attempts. Populated by the email.Sender when SMTP returns a
-- permanent-failure code (5xx) on send; later by provider webhooks
-- (SES/SendGrid) plugging into the same customer.MarkEmailBounced
-- service method. 'unknown' is the default — we've never attempted a
-- send or never observed a delivery signal.
--
-- Status values:
--   unknown     — default; no signal captured
--   ok          — last observed delivery was accepted
--   bounced     — permanent failure; do not retry
--   complained  — recipient flagged as spam (provider-webhook-driven)
ALTER TABLE customers ADD COLUMN email_status TEXT NOT NULL DEFAULT 'unknown'
  CHECK (email_status IN ('unknown', 'ok', 'bounced', 'complained'));
ALTER TABLE customers ADD COLUMN email_last_bounced_at TIMESTAMPTZ;
ALTER TABLE customers ADD COLUMN email_bounce_reason TEXT;
