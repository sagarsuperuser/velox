-- Webhook secret rotation with grace period (industry-standard, Stripe-parity).
--
-- When an endpoint's signing secret is rotated, the previous secret is copied
-- into secondary_secret_* columns with a 72-hour expires_at. During that
-- window the dispatcher signs every outbound request with BOTH secrets —
-- emitted as two `v1=` entries inside a single Velox-Signature header, the
-- same multi-signature format Stripe uses. Receivers that haven't yet
-- deployed the new verifier can pass if ANY signature matches, so partners
-- can stage a code change without a production outage.
--
-- After expires_at passes, the secondary is skipped at sign time (it stays
-- in the row as cold data until the next rotation overwrites it). NULL
-- expires_at or NULL secondary_secret_encrypted means single-secret mode —
-- the default state and the post-expiry state.
ALTER TABLE webhook_endpoints ADD COLUMN secondary_secret_encrypted TEXT;
ALTER TABLE webhook_endpoints ADD COLUMN secondary_secret_last4 TEXT;
ALTER TABLE webhook_endpoints ADD COLUMN secondary_secret_expires_at TIMESTAMPTZ;
