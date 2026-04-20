-- FEAT-3 P4.5: blind index on customers.email so the magic-link flow can
-- look a customer up by email without the DB ever holding plaintext.
--
-- Why a separate column: customers.email is AES-256-GCM encrypted at rest
-- with a random nonce (migration 0011+), so identical plaintexts hash to
-- different ciphertexts and WHERE email = $1 is impossible. email_bidx is
-- HMAC-SHA256(VELOX_EMAIL_BIDX_KEY, lower(email)) — deterministic, keyed,
-- and only useful to an attacker who also has the HMAC key.
--
-- Forward-only schema: existing rows get NULL and stay unfindable until the
-- operator runs cmd/velox-backfill-email-bidx, which decrypts each email
-- with VELOX_ENCRYPTION_KEY and writes the index. Backfilling in SQL isn't
-- possible because decryption lives in Go.

ALTER TABLE customers
    ADD COLUMN email_bidx TEXT;

COMMENT ON COLUMN customers.email_bidx IS
    'HMAC-SHA256(VELOX_EMAIL_BIDX_KEY, lower(email)). Blind index used for magic-link email lookup; encrypted-at-rest email is not directly queryable.';

-- Non-unique index: the same email may belong to customers across different
-- tenants (a shared operator email, a contractor on several accounts). The
-- magic-link handler sends one link per (tenant, customer) match.
CREATE INDEX idx_customers_email_bidx
    ON customers (email_bidx)
    WHERE email_bidx IS NOT NULL;
