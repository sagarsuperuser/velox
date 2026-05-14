-- The ALTER TABLE statements use IF EXISTS on the table reference too,
-- not just the column. Migration 0068 (forward-only) drops the `users`
-- table outright as part of the auth-revert cleanup; without IF EXISTS
-- on the table, a full rollback (0080 → 0) past 0068's down (which is
-- intentionally a no-op) would error here on a vanished table.
-- Postgres supports `ALTER TABLE IF EXISTS … DROP COLUMN IF EXISTS …`
-- — both halves are needed: outer guards the table, inner guards the
-- column when 0034 is being re-rolled-back after a partial state.
DROP TABLE IF EXISTS password_reset_tokens;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS user_tenants;
ALTER TABLE IF EXISTS users DROP COLUMN IF EXISTS email_verified_at;
ALTER TABLE IF EXISTS users DROP COLUMN IF EXISTS password_hash;
