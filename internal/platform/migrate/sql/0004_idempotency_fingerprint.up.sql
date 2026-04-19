-- Idempotency request fingerprint
--
-- Stripe's idempotency contract: a key, once used, binds to the exact request
-- (method + path + body). Reusing the key with different parameters must
-- fail with 422, not silently replay the first response. Without this column
-- a client bug like "retrying POST /invoices with a new amount under the old
-- key" would return the old invoice and mask the bug.
--
-- Column is nullable so rows written before this migration don't break the
-- uniqueness check — they simply skip the fingerprint comparison. New rows
-- populate it. After 24h (idempotency_keys.expires_at default), all rows
-- have a fingerprint and the NULL path is unreachable.

ALTER TABLE idempotency_keys
    ADD COLUMN request_fingerprint BYTEA;

-- Index not added: the fingerprint is only read after we've already located
-- the row by (tenant_id, key) — it's a value check, not a lookup key.
