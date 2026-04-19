-- SEC-2: encrypt webhook signing secrets at rest.
--
-- Today, webhook_endpoints.secret holds the raw whsec_<hex> signing key in
-- plaintext. A DB dump, a read replica compromise, or a careless backup
-- exposes every tenant's ability to forge webhook deliveries against their
-- own receivers (signatures would still verify).
--
-- The fix: store AES-256-GCM ciphertext in secret_encrypted (TEXT with the
-- "enc:<base64(nonce||ciphertext)>" envelope produced by internal/platform/
-- crypto). Keep a last-4 suffix of the plaintext in secret_last4 so the UI
-- can identify a key without ever showing the full value. The raw secret is
-- only returned once, at create/rotate time, as today.
--
-- Plaintext rows cannot be migrated forward (we don't have the plaintext to
-- re-encrypt). This is pre-production — no external receivers are registered
-- against the local DB yet — so we wipe the webhook tables and require
-- tenants to re-register endpoints after the migration. Children are
-- cleared first to respect FK RESTRICT from 0015.
DELETE FROM webhook_deliveries;
DELETE FROM webhook_events;
DELETE FROM webhook_outbox;
DELETE FROM webhook_endpoints;

ALTER TABLE webhook_endpoints DROP COLUMN secret;
ALTER TABLE webhook_endpoints ADD COLUMN secret_encrypted TEXT NOT NULL;
ALTER TABLE webhook_endpoints ADD COLUMN secret_last4 TEXT NOT NULL;
