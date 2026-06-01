-- Down is LOSSY: the raw plaintext tokens were dropped and cannot be recovered
-- from the hash (irreversible) or the ciphertext (app key not in the DB). The
-- column is re-added empty; existing hosted-invoice URLs stop resolving until
-- the tokens are rotated. Acceptable for a rollback of a security migration.
ALTER TABLE invoices ADD COLUMN public_token text;
CREATE UNIQUE INDEX idx_invoices_public_token
    ON invoices (public_token)
    WHERE public_token IS NOT NULL;

DROP INDEX IF EXISTS idx_invoices_public_token_hash;
ALTER TABLE invoices DROP COLUMN public_token_hash;
ALTER TABLE invoices DROP COLUMN public_token_encrypted;
