-- Reverting encryption means going back to plaintext storage, but we can't
-- recover the plaintext from ciphertext without the key. Wipe the webhook
-- tables (matching the up migration's precondition) and restore the column.
DELETE FROM webhook_deliveries;
DELETE FROM webhook_events;
DELETE FROM webhook_outbox;
DELETE FROM webhook_endpoints;

ALTER TABLE webhook_endpoints DROP COLUMN secret_encrypted;
ALTER TABLE webhook_endpoints DROP COLUMN secret_last4;
ALTER TABLE webhook_endpoints ADD COLUMN secret TEXT NOT NULL;
