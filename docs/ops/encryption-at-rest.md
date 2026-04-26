# Encryption-at-Rest Verification Guide

Companion to [secrets-management.md](./secrets-management.md) and
[audit-log-retention.md](./audit-log-retention.md).
`secrets-management.md` answers "where do the keys come from?";
`audit-log-retention.md` answers "what evidence do we keep?";
this doc answers "what does Velox actually encrypt at rest, with which
key, and how do you prove on a running install that the encryption is
in effect?"

Velox encrypts the small handful of fields whose plaintext value is
load-bearing if a database dump leaks: customer PII, webhook signing
secrets, and per-tenant Stripe credentials. Every other column on disk
is protected by the operator's Postgres deployment — disk-level
encryption (AWS RDS storage encryption, GCP CMEK, LUKS on a
self-hosted VM) is the second layer the application encryption layer
is built on top of, not a substitute for. This guide tells you which
columns hold ciphertext, which hold deterministic blind indexes,
which hold one-way hashes, which hold plaintext on purpose, and how
to verify each of those statements is true on your running install.

## Table of contents

- [What's encrypted](#whats-encrypted)
- [The crypto primitives](#the-crypto-primitives)
- [Verification recipes](#verification-recipes)
- [Key management](#key-management)
- [What's NOT encrypted (and why)](#whats-not-encrypted-and-why)
- [Compliance mapping](#compliance-mapping)
- [Configuration knobs](#configuration-knobs)

---

## What's encrypted

Application-layer encryption applies to the columns below. Everything
else relies on storage-layer encryption (see "What's NOT encrypted"
further down).

| Asset | Algorithm | Key | Table.Column | Notes |
|---|---|---|---|---|
| Customer email | AES-256-GCM | `VELOX_ENCRYPTION_KEY` | `customers.email` | Encrypted on `Create`/`Update`, decrypted on every read in `internal/customer/postgres.go::encryptCustomer` / `decryptCustomer`. |
| Customer display name | AES-256-GCM | `VELOX_ENCRYPTION_KEY` | `customers.display_name` | Same path as `email`. |
| Customer email blind index | HMAC-SHA256 (hex) | `VELOX_EMAIL_BIDX_KEY` | `customers.email_bidx` | Deterministic keyed hash of `lower(trim(email))`. Lets the magic-link flow look up a customer by email without ever decrypting the ciphertext column. Not encryption — a one-way keyed hash. See migration `0023_customers_email_bidx`. |
| Billing-profile legal name | AES-256-GCM | `VELOX_ENCRYPTION_KEY` | `customer_billing_profiles.legal_name` | Encrypted in `internal/customer/postgres.go::encryptBillingProfile`. |
| Billing-profile email | AES-256-GCM | `VELOX_ENCRYPTION_KEY` | `customer_billing_profiles.email` | Same path. |
| Billing-profile phone | AES-256-GCM | `VELOX_ENCRYPTION_KEY` | `customer_billing_profiles.phone` | Same path. |
| Billing-profile tax ID | AES-256-GCM | `VELOX_ENCRYPTION_KEY` | `customer_billing_profiles.tax_id` | Same path. The companion `tax_id_type` is metadata only (e.g. `"eu_vat"`, `"us_ein"`) and is **not** encrypted — it's the schema kind, not the secret. |
| Outbound webhook signing secret (primary) | AES-256-GCM | `VELOX_ENCRYPTION_KEY` | `webhook_endpoints.secret_encrypted` | Stored as `enc:<base64(nonce\|ciphertext)>`; the matching `secret_last4` column is the unencrypted last 4 chars used by the dashboard to identify a key. See migration `0019_webhook_secret_encrypt` for the rewrite that wiped pre-existing plaintext rows. |
| Outbound webhook signing secret (secondary, rotation grace) | AES-256-GCM | `VELOX_ENCRYPTION_KEY` | `webhook_endpoints.secondary_secret_encrypted` | The `RotateEndpointSecret` flow stashes the prior secret here for `secondary_secret_expires_at`, encrypted with the same key. |
| Per-tenant Stripe secret API key | AES-256-GCM | `VELOX_ENCRYPTION_KEY` | `stripe_provider_credentials.secret_key_encrypted` | One row per `(tenant_id, livemode)`. Companion `secret_key_last4` and `secret_key_prefix` are display-only — they exist so the UI can render `sk_live_51ab••••••••wxyz` without reading the ciphertext. See `internal/tenantstripe/postgres.go`. |
| Per-tenant Stripe webhook signing secret | AES-256-GCM | `VELOX_ENCRYPTION_KEY` | `stripe_provider_credentials.webhook_secret_encrypted` | Nullable: a tenant can register API keys before registering the webhook. The `Stripe-Signature` verifier on inbound webhooks calls `Store.GetByID` which decrypts this on the fly. |

The five env vars in the table above collapse to two distinct secrets:
the AES key (`VELOX_ENCRYPTION_KEY`) is shared across every encrypted
field, and the HMAC key (`VELOX_EMAIL_BIDX_KEY`) is only used by the
email blind index. They live in two different env vars on purpose so
a compromise of one doesn't automatically reveal the other — see
"Key management" below.

### What's hashed, not encrypted

A few asset categories use one-way hashing instead of two-way
encryption. The distinction matters for compliance posture: hashed
values are unrecoverable even with the key, so the threat model is
different from "encrypted with a rotatable key."

| Asset | Algorithm | Stored where | Why hashed not encrypted |
|---|---|---|---|
| API keys (`vlx_secret_live_…`, `vlx_publishable_test_…`, `vlx_platform_…`) | SHA-256 with a 16-byte per-key random salt | `api_keys.key_hash` (+ `api_keys.key_salt`) | Velox never needs to recover the plaintext — verifying a key is hashing the presented value and comparing constant-time. Storing the plaintext (or even a reversible ciphertext) would let a DB compromise issue API requests under any tenant's identity. The matching `api_keys.key_prefix` (12 chars including the `vlx_secret_live_` type/mode prefix) is plaintext on purpose so the dashboard can show a key by its prefix without exposing the full secret. See `internal/auth/service.go::CreateKey`. |
| Dashboard user passwords | Argon2id (PHC string, m=64MiB, t=3, p=4, 16-byte salt, 32-byte key) | `users.password_hash` | OWASP 2024 baseline for general server use. The PHC encoding (`$argon2id$v=19$m=…,t=…,p=…$<salt>$<hash>`) is self-describing so future tuning of the work parameters doesn't invalidate existing rows. See `internal/user/password.go`. |
| Dashboard sessions | SHA-256 of the random session id | `sessions.id_hash` | Cookie carries the raw id; a DB snapshot can't be replayed as a bearer token. Migration `0034_embedded_auth.up.sql`. |
| Password-reset tokens | SHA-256 of the random token | `password_reset_tokens.token_hash` | Single-use, short-TTL. Same rationale as `sessions`. |
| Customer-portal magic links | SHA-256 of the random token | `customer_portal_magic_links.token_hash` | Single-use, 15-min TTL. The raw `vlx_cpml_…` token is in the email body once and never persisted. Migration `0024_customer_portal_magic_links.up.sql`. |
| Customer-portal sessions | SHA-256 of the random session id | `customer_portal_sessions.token_hash` | Same rationale as dashboard sessions, customer-side. |
| Payment-update tokens | SHA-256 of the random token | `payment_update_tokens.token_hash` | The token is what an end customer clicks in a dunning email; the Velox API only ever sees the hash. See `internal/payment/token.go`. |

### Plaintext on purpose

A handful of high-entropy public tokens are stored in the clear because
the token *is* the URL — a URL is shareable by design and any
verification model that "decrypts on read" defeats the share-by-URL
property.

| Asset | Stored where | Rationale |
|---|---|---|
| Hosted-invoice public token (`vlx_pinv_<32-hex>`) | `invoices.public_token` | 256 bits of entropy. The token is the credential the public hosted-invoice page uses. Encrypting it would mean the API server has to decrypt every public-page request before serving it — a hot path that gains no security over rate-limited URL guessing on a 256-bit space. See migration `0048_invoice_public_token.up.sql`. |
| Stripe publishable key (`pk_live_…`, `pk_test_…`) | `stripe_provider_credentials.publishable_key` | Stripe's data classification: publishable keys are public credentials by Stripe's design — they're embedded in client-side JS. Encrypting them would imply a sensitivity they don't have. |
| Stripe credential "key prefix" (12-char display value) | `stripe_provider_credentials.secret_key_prefix` | Display-only, matches what tenants see in their Stripe dashboard. The 12 chars (e.g. `sk_live_51ab`) cover the type prefix plus 4 account-identifying chars — too short to be useful to an attacker. |

---

## The crypto primitives

The two primitives live in `internal/platform/crypto/crypto.go`:

### `Encryptor` — AES-256-GCM with random nonce

```go
type Encryptor struct {
    key []byte // nil = noop
}

func NewEncryptor(hexKey string) (*Encryptor, error)
func (e *Encryptor) Encrypt(plaintext string) (string, error)
func (e *Encryptor) Decrypt(value string) (string, error)
```

Key contract:

- `VELOX_ENCRYPTION_KEY` is **64 hex characters = 32 bytes = 256 bits**.
  `NewEncryptor` rejects anything else with a clear error
  (`encryption key must be exactly 64 hex characters (32 bytes), got %d`).
- Validation runs at startup in `internal/config/config.go`. In
  production (`APP_ENV=production`), missing or malformed
  `VELOX_ENCRYPTION_KEY` is **fatal**: the binary refuses to start with
  `VELOX_ENCRYPTION_KEY is required in production — refusing to start with plaintext PII storage`.
  In `local` and `staging` it's a `slog.Warn` and the binary falls back
  to the noop encryptor (see "What happens without a key" below).

Ciphertext envelope:

- Output of `Encrypt` is the literal string `enc:<base64(nonce ‖ ciphertext)>`.
  The 12-byte AES-GCM nonce is generated via `crypto/rand` per call,
  so encrypting the same plaintext twice yields different ciphertexts —
  this is what defeats the trivial "lookup by ciphertext" attack but is
  also why deterministic equality search needs the separate `Blinder`.
- `Decrypt` is permissive on read: a value without the `enc:` prefix is
  returned as-is. That's the migration affordance — historical rows
  written before encryption was enabled still decrypt cleanly. The
  reverse is not true: a noop encryptor presented with an `enc:` value
  errors with `encrypted value found but no encryption key configured`,
  so a key rollback that loses the key fails loud.

### `Blinder` — HMAC-SHA256 keyed hash

```go
type Blinder struct {
    key []byte // nil = noop
}

func NewBlinder(hexKey string) (*Blinder, error)
func (b *Blinder) Blind(value string) string
```

- `VELOX_EMAIL_BIDX_KEY` is also **64 hex characters = 32 bytes**, same
  validation shape as the encryption key, distinct value.
- `Blind(value)` returns `hex(HMAC-SHA256(key, value))`. Deterministic
  by design — that's the whole point: the magic-link handler needs
  `WHERE email_bidx = $1` to work without first reading the encrypted
  ciphertext on every customer row. Locked down by `TestBlinderDeterministicAndKeyed`
  in `internal/platform/crypto/crypto_test.go`.
- The two keys are independent on purpose. An attacker who steals
  `VELOX_EMAIL_BIDX_KEY` can compute the blind index for any email
  they can guess (a dictionary attack against `WHERE email_bidx = $X`)
  but cannot decrypt the actual `email` ciphertext column. An attacker
  who steals `VELOX_ENCRYPTION_KEY` can decrypt every row but cannot
  enumerate by email without also stealing the blinder key. Compromise
  of either alone is materially worse than the status quo; compromise
  of both is the same as plaintext storage. Rotate them on independent
  schedules so a single leaked artifact doesn't reveal both.

---

## Verification recipes

Copy-pasteable SQL to run on a live database to prove each part of the
encryption posture is in effect. Run every recipe at least once on a
fresh staging install, and at least quarterly on production as part of
the SOC 2 evidence cadence.

### 1. Customer PII is ciphertext, not plaintext

The single most important check. If this returns readable email
addresses, encryption-at-rest for PII is **not** in effect on this
install — investigate before continuing.

```sql
-- Sample 5 customer rows. Both email and display_name should be
-- "enc:<base64>" envelopes, not readable values.
SELECT id, tenant_id,
       LEFT(email, 8)       AS email_envelope,
       LEFT(display_name,8) AS name_envelope,
       LENGTH(email)        AS email_len,
       LENGTH(display_name) AS name_len
FROM customers
WHERE email <> '' OR display_name <> ''
ORDER BY created_at DESC
LIMIT 5;
```

Expected: every non-empty `email_envelope` and `name_envelope` starts
with `enc:`. The `_len` columns are bonus signal — AES-GCM ciphertext
plus the `enc:` prefix and base64 expansion is at least ~50 chars even
for a one-character plaintext, so any short value is suspicious.

A row written before encryption was enabled (or under a noop encryptor
in dev) has a plaintext value here. The next recipe scans for those.

### 2. Find any plaintext rows that slipped past encryption

```sql
-- Customers whose email column doesn't have the enc: envelope.
-- Should return 0 in production. A non-zero result means rows were
-- written under a noop encryptor — usually because the binary started
-- without VELOX_ENCRYPTION_KEY and config.go did NOT refuse to boot
-- (i.e. APP_ENV was not "production" at the time, or the var was
-- unset between deploys).
SELECT count(*) AS plaintext_email_rows
FROM customers
WHERE email <> '' AND email NOT LIKE 'enc:%';

SELECT count(*) AS plaintext_name_rows
FROM customers
WHERE display_name <> '' AND display_name NOT LIKE 'enc:%';

-- Same shape, billing profiles.
SELECT
  count(*) FILTER (WHERE legal_name <> '' AND legal_name NOT LIKE 'enc:%') AS plaintext_legal_name,
  count(*) FILTER (WHERE email      <> '' AND email      NOT LIKE 'enc:%') AS plaintext_billing_email,
  count(*) FILTER (WHERE phone      <> '' AND phone      NOT LIKE 'enc:%') AS plaintext_phone,
  count(*) FILTER (WHERE tax_id     <> '' AND tax_id     NOT LIKE 'enc:%') AS plaintext_tax_id
FROM customer_billing_profiles;
```

If any of these are non-zero on a production install, the plaintext
rows need to be re-encrypted. There is no built-in re-encryption
command today (see "Key management" → "Key rotation: not implemented"
below). The remediation path is a one-shot script that reads each row
under `TxBypass`, calls `s.enc.Encrypt(plaintext)`, and writes the
ciphertext back.

### 3. Webhook signing secrets are encrypted

```sql
-- Outbound webhook endpoints. Every active row must have an enc:
-- envelope on the primary secret column.
SELECT count(*) FILTER (WHERE secret_encrypted NOT LIKE 'enc:%')
       AS plaintext_outbound_secrets
FROM webhook_endpoints
WHERE active = true;

-- Per-tenant Stripe secrets. Every row must have an enc: envelope on
-- secret_key_encrypted; webhook_secret_encrypted is nullable but
-- non-null values must also be envelopes.
SELECT
  count(*) FILTER (WHERE secret_key_encrypted NOT LIKE 'enc:%')
    AS plaintext_stripe_keys,
  count(*) FILTER (WHERE webhook_secret_encrypted IS NOT NULL
                     AND webhook_secret_encrypted NOT LIKE 'enc:%')
    AS plaintext_stripe_webhook_secrets
FROM stripe_provider_credentials;
```

All three counts should be 0. A non-zero result in either
`stripe_provider_credentials` column means a tenant connected Stripe
under a noop encryptor and their secrets are sitting in plaintext on
disk; rotate the credential through the Stripe dashboard, paste the
new values into Velox under a properly configured binary, and the new
row will land encrypted.

### 4. The blind index is populated

```sql
-- Every customer with a non-empty email should have a non-empty
-- email_bidx. A non-zero result means the blinder was unconfigured at
-- the time the row was written — those customers cannot be located by
-- the magic-link flow.
SELECT count(*) AS unindexed_emails
FROM customers
WHERE email IS NOT NULL AND email <> ''
  AND (email_bidx IS NULL OR email_bidx = '');

-- Spot-check the index shape. Every value should be exactly 64 hex
-- chars (HMAC-SHA256 hex digest).
SELECT id, LENGTH(email_bidx) AS bidx_len
FROM customers
WHERE email_bidx IS NOT NULL
LIMIT 5;
```

Expected: `unindexed_emails = 0` on a healthy install, and every
`bidx_len = 64`.

### 5. Confirm AES-GCM decrypt round-trips through the live binary

The DB-only recipes above prove that ciphertext is on disk; they don't
prove that the binary can read it. Use the API to round-trip a known
customer:

```bash
# 1. Mint or fetch a customer with a known plaintext email/name.
curl -sS -H "Authorization: Bearer $VELOX_API_KEY" \
     -H 'Content-Type: application/json' \
     -X POST "$VELOX_BASE_URL/v1/customers" \
     -d '{"external_id":"verify-encryption-2026-04","email":"verify@example.com","display_name":"Encryption Verify"}'

# 2. Read it back. The response surfaces decrypted plaintext if the
#    binary holds the right VELOX_ENCRYPTION_KEY.
curl -sS -H "Authorization: Bearer $VELOX_API_KEY" \
     "$VELOX_BASE_URL/v1/customers?external_id=verify-encryption-2026-04"

# 3. Confirm the row on disk is ciphertext, not the value the API returned.
psql "$DATABASE_URL" -c "
  SELECT email, display_name FROM customers
   WHERE external_id = 'verify-encryption-2026-04';
"
# Expected: enc:<base64> envelope on both columns.
```

If step 2 returns the expected plaintext but step 3 shows `enc:`
ciphertext, encryption-at-rest is fully wired through both halves of
the request lifecycle. If step 2 returns a 500 with
`decrypt customer email: …`, the binary holds a different
`VELOX_ENCRYPTION_KEY` than the one the row was encrypted with — the
key was rotated without re-encrypting, or two binaries with mismatched
keys are pointed at the same DB.

### 6. Detect a noop encryptor on a running binary

The startup log line is the single most reliable signal:

```bash
# In production, this exact line means encryption is in effect:
journalctl -u velox -n 200 | grep "encryption at rest enabled"
# Expected: "encryption at rest enabled for customer PII, webhook secrets, and Stripe credentials"

# This line means encryption is OFF:
journalctl -u velox -n 200 | grep "VELOX_ENCRYPTION_KEY not set"
# Expected (on a healthy production install): no match.
# Any match on a production install is a SEV-1: PII is being written
# in plaintext.
```

A complementary SQL check, agnostic to log access:

```sql
-- A binary running under the noop encryptor will append rows whose
-- email/name columns are plaintext. Compare counts on rows created in
-- the last hour vs older — a sudden plaintext-row appearance after a
-- deploy means the new binary is starting without the env var.
SELECT date_trunc('hour', created_at) AS hr,
       count(*) FILTER (WHERE email LIKE 'enc:%')      AS encrypted,
       count(*) FILTER (WHERE email NOT LIKE 'enc:%' AND email <> '') AS plaintext
FROM customers
WHERE created_at > now() - INTERVAL '24 hours'
GROUP BY 1 ORDER BY 1 DESC;
```

If `plaintext > 0` in the most recent buckets but `encrypted` dominates
older buckets, the most recent restart was misconfigured. Roll back to
the previous known-good config before continuing.

### 7. Postgres-level disk encryption (storage layer)

Velox does not, and cannot, attest to storage-layer encryption from
inside the binary — that's an infrastructure-side fact. The recipes
below are the operator-side verification for the most common
deployment shapes:

```bash
# AWS RDS — verify storage encryption is enabled, and which KMS key
# encrypts the volumes.
aws rds describe-db-instances \
  --db-instance-identifier "$VELOX_DB_INSTANCE" \
  --query 'DBInstances[0].{StorageEncrypted:StorageEncrypted, KmsKeyId:KmsKeyId}'

# GCP Cloud SQL — disk encryption with a customer-managed key (CMEK).
gcloud sql instances describe "$VELOX_DB_INSTANCE" \
  --format='value(diskEncryptionConfiguration.kmsKeyName)'

# Self-hosted — LUKS on the data volume.
sudo cryptsetup status "$(findmnt -no SOURCE /var/lib/postgresql)"
# Expected: "type: LUKS2", "cipher: aes-xts-plain64".
```

The Velox audit posture for the storage layer is "the operator is
responsible." We don't claim disk encryption in the SOC 2 mapping
table below unless your hosting layer attests to it — see the
"Compliance mapping" section.

---

## Key management

### Key sources

Both keys come from the environment. `internal/api/router.go`
`NewServer` reads them at startup:

```go
if encKey := os.Getenv("VELOX_ENCRYPTION_KEY"); encKey != "" {
    enc, _ := crypto.NewEncryptor(encKey)
    customerStore.SetEncryptor(enc)
    tenantStripeStore.SetEncryptor(enc)
    webhookOutStore.SetEncryptor(enc)
}
if bidxKey := os.Getenv("VELOX_EMAIL_BIDX_KEY"); bidxKey != "" {
    b, _ := crypto.NewBlinder(bidxKey)
    customerStore.SetBlinder(b)
}
```

The same `Encryptor` instance is wired into the customer store, the
tenantstripe store, and the webhook-out store, so a key rotation flows
through every encrypted column uniformly. The blinder only attaches to
the customer store because the email blind index is the only place in
the system that uses it.

The standard delivery path for both env vars on a production install
is whatever your secrets-management tooling provides — see
[secrets-management.md](./secrets-management.md) for the External
Secrets Operator + AWS Secrets Manager / Vault setup.

### Generating fresh keys

Both keys are 32 random bytes hex-encoded. The recipe is identical:

```bash
# AES-256 encryption key
openssl rand -hex 32 > /tmp/velox-encryption-key
# Email blind-index key — a separate openssl invocation so the kernel's
# random pool advances and the two keys are truly independent.
openssl rand -hex 32 > /tmp/velox-email-bidx-key

# Confirm length: 64 hex chars each.
wc -c /tmp/velox-*-key
# Expected: 65 (64 + newline) for each line.
```

Push these into your secrets store; never commit them to source control,
never write them to a developer's `.env` for production access. The
`.env.example` shipped in the repo lists both vars with `openssl rand
-hex 32` as the recommended generator.

### Key rotation: NOT IMPLEMENTED today

> **Note — gap honest disclosure.** Velox has no built-in mechanism to
> rotate `VELOX_ENCRYPTION_KEY` or `VELOX_EMAIL_BIDX_KEY`. Setting a
> new value in the environment **breaks decryption** of every row
> encrypted under the old value. There is no envelope-encryption layer
> (DEK/KEK), no key id stored alongside ciphertext, and no
> re-encryption command.

What this means in practice:

- **`VELOX_ENCRYPTION_KEY`**: rotating the value flips every existing
  `enc:` row to "ciphertext written under a key the binary no longer
  has." `Decrypt` returns `decrypt: cipher: message authentication
  failed`, the customer/billing-profile/webhook-secret read paths
  return 500, and the API surface for those tenants stops working.
- **`VELOX_EMAIL_BIDX_KEY`**: rotating the value silently changes the
  HMAC output. Existing `email_bidx` rows still resolve, but any new
  customer is indexed under the new key — and the magic-link flow
  cannot find rows indexed under either old or new because it only
  queries the current blind. The operational symptom is "magic-link
  email lookup fails for everyone" rather than a hard error.
- **`secrets-management.md`** lists `VELOX_ENCRYPTION_KEY` as
  `Cannot rotate without re-encrypting data. Plan a migration.` —
  that's the same gap, called out from the secrets side.

The proper fix is envelope encryption: each row stores the ciphertext
plus the id of the data-encryption key (DEK) it was encrypted with,
and the DEK is itself encrypted by a key-encryption key (KEK) loaded
from the env. Rotating the KEK only requires re-encrypting the small
number of DEK rows; rotating a DEK is per-row but can run in
background batches. This is the design Stripe / AWS KMS / GCP KMS use
and is the right Velox endgame.

For now, treat both keys as if they're permanent. If a key is
compromised, rotation requires:

1. A maintenance window with the API in read-only mode.
2. A one-shot script that reads every encrypted row under
   `TxBypass`, decrypts with the old key, encrypts with the new key,
   and writes the ciphertext back.
3. An equivalent script for `customers.email_bidx` that recomputes
   `HMAC(new_key, lower(email))` for every row.
4. A coordinated env-var swap and binary restart.

That migration tooling is **not** built. Document the gap in your
SOC 2 control narrative and your incident-response runbook; flag a
re-evaluation when a tenant's compliance regime forces the issue.

### What to do if a key is exposed

The action depends on the threat model:

- **`VELOX_ENCRYPTION_KEY` leaked** — an attacker with the key plus a
  copy of the database can decrypt every PII column, every webhook
  signing secret, and every per-tenant Stripe secret key. They cannot
  hit your live API without also possessing valid API keys, and they
  cannot drain Stripe accounts without also possessing the Stripe
  webhook events and a way to issue charges (which would route through
  the tenant's own Stripe account, audited there). Treat as a SEV-1
  data-confidentiality incident: notify tenants, rotate all webhook
  signing secrets via `POST /v1/webhook-endpoints/{id}/rotate`, ask
  every tenant to rotate their Stripe secret key in their Stripe
  dashboard, and plan the bulk re-encryption migration above.
- **`VELOX_EMAIL_BIDX_KEY` leaked** — an attacker can compute the
  blind index for any email they can guess and probe whether a
  customer exists for that email across tenants. They cannot decrypt
  the email ciphertext or do anything with the customer record beyond
  confirming presence. SEV-2 — privacy regression but not a data
  breach in the GDPR Art. 4 sense. Plan a key rotation in the next
  maintenance window.
- **Both leaked together** — equivalent to plaintext storage. SEV-1
  with the same response as the encryption-key scenario above.

---

## What's NOT encrypted (and why)

The fields below are stored in plaintext in the application schema.
For each, document that the fact is intentional and what defense layer
covers it.

### Postgres rows generally — operator's storage encryption

Plans, subscriptions, invoices, line items, payments, credit ledger
entries, usage events, audit log entries, recipes, coupons, tenant
settings, etc. — none of this is application-layer encrypted. The
defense layer is the operator's Postgres deployment:

- **AWS RDS / Aurora** — storage is AES-256 encrypted at rest with
  AWS-managed or customer-managed (KMS CMK) keys when
  `StorageEncrypted=true` is set on the instance.
- **GCP Cloud SQL** — encrypted at rest by default; supply a
  customer-managed key (CMEK) for tenants who require it.
- **Self-hosted on a single VM** — encrypt the data volume with LUKS
  or your filesystem-of-choice's at-rest encryption (ext4
  `fscrypt`, ZFS native, etc.).

Velox does not encrypt these fields at the application layer because
(a) none of them carry secrets in the threat-model sense — invoice
amounts and timestamps are operationally visible by design, customer
ids are opaque random strings, plan ids are public configuration —
and (b) encrypting hot paths like the billing engine's per-cycle
invoice generation would introduce per-row decrypt/encrypt overhead
on the most write-heavy tables (`usage_events`, `invoice_line_items`)
without commensurate confidentiality benefit.

### Audit log entries

`audit_log` is plaintext at the application layer. Audit rows
reference IDs and labels — `metadata.path = '/v1/customers/cus_abc'`,
`resource_label = 'Acme Corp'` — but never raw PII like an email
address or a Stripe secret. The append-only trigger
(migration `0011_audit_append_only`) protects the rows from
modification; the storage layer protects them from disk-image attacks.
Note that `audit_log.actor_id` resolves through `api_keys.name` to a
human-set label that may be a person's name; treat raw audit-log
exports as personal data per
[audit-log-retention.md](./audit-log-retention.md#a-note-on-pii).

### Idempotency keys

`idempotency_keys.idempotency_key` is the value the customer chose. We
hash it (SHA-256) into `idempotency_keys.idempotency_key_fingerprint`
for collision detection (migration `0004_idempotency_fingerprint`),
but the original value is plaintext for the lookup path. Idempotency
keys are not secrets — they're shared between the client and Velox by
design, and Stripe's equivalent column is also plaintext.

### Stripe customer / payment-method ids

`customer_payment_setups.stripe_customer_id` (`cus_…`) and
`stripe_payment_method_id` (`pm_…`) are plaintext. They're opaque
Stripe identifiers, not secrets — anyone who can list a Stripe
account's customers sees these directly. Card data
(`card_brand`, `card_last4`, `card_exp_*`) is the metadata Stripe
returns and is intentionally non-sensitive (PCI's "non-cardholder
data"). The actual card lives in Stripe.

### IP addresses

`audit_log.ip_address` is plaintext. WP29 / EDPB classify IP
addresses as personal data, but the audit log's accountability purpose
requires the IP be correlatable to a request, which makes hashing or
encryption defeat the purpose. The retention window in
[audit-log-retention.md](./audit-log-retention.md#gdpr--eu-privacy)
is the GDPR mitigation.

### API key prefix, last4 columns

`api_keys.key_prefix`, `webhook_endpoints.secret_last4`,
`stripe_provider_credentials.secret_key_last4` /
`secret_key_prefix` /
`webhook_secret_last4` — all plaintext, all on purpose. Each is a
display-only fragment used by the dashboard to identify a key without
re-exposing the full secret. The full secret is hashed
(`api_keys.key_hash`) or encrypted (`*_encrypted` companion columns).

---

## Compliance mapping

How the encryption story above maps onto the named control families.
Cite this section in your SOC 2 narrative; copy the relevant rows into
your data-mapping document.

### SOC 2 — CC6.1 / CC6.7 (encryption in transit + at rest)

CC6.1 ("the entity implements logical access security software …") and
CC6.7 ("transmission of data") together require **encryption of
sensitive data in transit and at rest**. Velox satisfies the
at-rest half via:

- AES-256-GCM on customer PII, webhook signing secrets, and per-tenant
  Stripe credentials (this doc's "What's encrypted" table).
- Argon2id on dashboard user passwords; SHA-256 on session ids,
  password-reset tokens, magic-link tokens, payment-update tokens, and
  API keys (this doc's "What's hashed, not encrypted" table).
- Storage-layer encryption on every other table is the operator's
  responsibility — name your KMS key id and AES-256-XTS / AES-256-CBC
  algorithm choice in your control narrative.

In-transit encryption is out of scope for this doc — see
[self-host.md](../self-host.md) on TLS termination via ALB / Cloudflare
/ certbot.

### PCI-DSS — Requirement 3 (protect stored cardholder data)

Velox does not store cardholder data. The Velox database holds
**Stripe payment-method tokens**, never PANs. PCI Requirement 3 ("do
not store sensitive authentication data after authorization") is
satisfied by Stripe holding the card and Velox holding only the token.

That said, PCI Requirement 3.5 ("document and implement procedures to
protect cryptographic keys used for encryption of cardholder data
against disclosure and misuse") flows through to the keys that
**protect the credentials that authorize the cardholder data system**
— which for Velox is the per-tenant Stripe secret key in
`stripe_provider_credentials.secret_key_encrypted`. The encryption of
that column with `VELOX_ENCRYPTION_KEY` puts the secret on the right
side of PCI Req 3.5; the **gap** is the missing key-rotation tooling
(see "Key rotation: NOT IMPLEMENTED" above), which a strict PCI auditor
will flag. Document this gap explicitly in your PCI control narrative
and track the envelope-encryption rebuild as the long-term remediation.

### GDPR — Article 32 (security of processing)

Article 32 names "the pseudonymisation and encryption of personal
data" as one of the appropriate technical and organisational measures
controllers must consider. Velox satisfies the encryption-of-PII half
via:

- Customer email + display name encrypted at rest (this doc).
- Billing-profile legal name + email + phone + tax id encrypted at rest.
- Magic-link tokens, session ids, password-reset tokens hashed at rest.

The blind index is **pseudonymisation** in the GDPR Art. 4(5) sense —
the email is replaced by an identifier (`HMAC(key, email)`) which can
no longer be attributed to a specific data subject without the
additional information (the HMAC key) which is kept separately and
subject to technical and organisational measures. That is the
textbook definition of pseudonymisation; cite it directly in your
Article 30 record-of-processing.

The plaintext-on-purpose columns (`audit_log.ip_address`,
`audit_log.metadata` carrying labels) are accountable under Article 5(2)
("controller shall be responsible for, and be able to demonstrate
compliance"). The audit log is itself personal data; the
[audit-log-retention guide](./audit-log-retention.md#gdpr--eu-privacy)
covers the retention window.

### HIPAA — §164.312(a)(2)(iv) (encryption / decryption of ePHI at rest)

Velox is not a healthcare system and does not store ePHI as a primary
data type. A Velox tenant whose own customers are covered entities may
have signed a BAA flowing HIPAA obligations through; in that case the
relevant HIPAA controls are:

- **§164.312(a)(2)(iv)** ("a mechanism to encrypt and decrypt
  electronic protected health information") — addressable
  implementation specification. Velox satisfies the at-rest portion
  for the PII columns above; the storage-layer encryption is the
  operator's responsibility for every other column.
- **§164.312(e)(2)(ii)** ("a mechanism to encrypt electronic protected
  health information whenever deemed appropriate") — same posture.
- **§164.308(a)(1)(ii)(D)** ("information system activity review") —
  audit log retention covered in the sibling guide.

Document the BAA flow-through tenant separately in your control
narrative — Velox's default encryption posture is "PII columns AES-GCM
at rest plus operator storage-layer encryption," which most BAA
auditors will accept once paired with a key-management policy.

---

## Configuration knobs

The complete env-var surface that affects encryption-at-rest behaviour.
Mirrors the shape used in [audit-log-retention.md](./audit-log-retention.md#configuration-knobs).

### Implemented today

| Env var | Purpose | Behaviour |
|---|---|---|
| `VELOX_ENCRYPTION_KEY` | AES-256-GCM key for PII / webhook secrets / Stripe credentials | 64 hex chars. **Required in production** — `internal/config/config.go::validateFatal` refuses to start when `APP_ENV=production` and the var is unset. In `local`/`staging`, missing → `slog.Warn` and noop encryptor. Invalid (wrong length, non-hex) is **fatal in every environment**. |
| `VELOX_EMAIL_BIDX_KEY` | HMAC-SHA256 key for `customers.email_bidx` | 64 hex chars. Currently a `slog.Warn` (not fatal) when unset, even in production — magic-link lookup fails closed without it but other surfaces work. **Recommended to make this fatal in production once magic-link adoption is non-zero**; tracked. |
| `APP_ENV` | Environment selector | `local` / `staging` / `production`. Production is the only mode that fails fatal on missing `VELOX_ENCRYPTION_KEY`. |

### Not implemented today

> The following are **gaps** — env-var surfaces that compliance-grade
> deployments would expect to find and that Velox does not yet expose.
> Document each in your SOC 2 narrative as "tracked future work."

| Future env var | Purpose | Why not yet |
|---|---|---|
| `VELOX_ENCRYPTION_KEY_ID` | Key id stored alongside each ciphertext envelope so multiple keys can coexist during rotation | Requires the envelope-encryption rebuild (DEK/KEK split). Same blocker as the rotation tooling. |
| `VELOX_KMS_KEK_ARN` (or equivalent for GCP / Vault) | KEK reference so the AES key never lives in plaintext in the binary's environment | Same envelope-encryption rebuild. |
| `VELOX_BLINDER_KEY_ID` | Versioning for the blind-index key so blind-index rotation is recoverable | Same; design will follow `VELOX_ENCRYPTION_KEY_ID`. |
| `VELOX_FORCE_ENCRYPTION_PRODUCTION` | Make `VELOX_EMAIL_BIDX_KEY` fatal in production | Cheap win; no design dependency; just hasn't been gated yet. Open a small PR to flip this once magic-link is on the critical path for any tenant. |

The gap that matters most is the envelope-encryption rebuild — it's
the single largest unlock for the rotation, KMS-integration, and
key-id columns above. Track it as one design effort, not four.

---

## Related docs

- [`secrets-management.md`](./secrets-management.md) — where the env
  vars come from in the first place. The 12-factor / External Secrets
  Operator setup, K8s Secret encryption at the etcd layer, RBAC. Also
  has the rotation table that calls out `VELOX_ENCRYPTION_KEY` as
  "cannot rotate without re-encrypting data" — this guide is the
  longer explanation.
- [`api-key-rotation.md`](./api-key-rotation.md) — paired with the
  SHA-256 hashing of `api_keys.key_hash` covered in "What's hashed,
  not encrypted." When a key is rotated, the old `key_hash` is
  retained until the grace expiry; the salt, hash, and prefix are all
  documented there.
- [`audit-log-retention.md`](./audit-log-retention.md) — the audit log
  is plaintext at the application layer (rationale in
  "What's NOT encrypted"); the retention windows there cover the GDPR
  / SOC 2 / HIPAA / SOX accountability obligations the encrypted-PII
  story is one half of.
- [`backup-recovery.md`](./backup-recovery.md) — backups inherit
  whatever encryption the source database has. Encrypt the backup
  destination at the storage layer (S3 SSE-KMS, GCS CMEK) — backups
  of an AES-encrypted DB volume restored to an unencrypted target
  defeat the at-rest posture.
- [`runbook.md`](./runbook.md#compliance) — alert / response postures
  cross-link both compliance docs from the on-call runbook.
- `docs/compliance/soc2-mapping.md`, `docs/compliance/gdpr-data-export.md`
  — landing in the rest of Week 10. The soc2 mapping doc will cite
  this document by section heading; the GDPR export doc will share
  the pseudonymisation language for `customers.email_bidx`.
