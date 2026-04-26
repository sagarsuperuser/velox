-- velox:no-transaction
--
-- GIN index on usage_events.properties — moved out of 0054 because
-- `CREATE INDEX … USING GIN` (the non-concurrent form) holds an
-- AccessExclusiveLock on `usage_events` for the entire build. The
-- populated-DB safety harness measured this at 53.5s on 5M rows
-- (docs/migration-safety-findings.md, 0054 entry); in production that
-- would freeze every concurrent insert into the largest table in the
-- schema for the same window.
--
-- CONCURRENTLY does not block writes — Postgres builds the index in two
-- passes against a snapshot taken under ShareUpdateExclusiveLock, then
-- catches up with concurrent changes. The trade-off is that CONCURRENTLY
-- cannot run inside a transaction block; this migration is therefore
-- marked `velox:no-transaction` so the runner applies it via autocommit.
-- See internal/platform/migrate/migrate.go for the runner mechanic.
--
-- IF NOT EXISTS is required because instances that already ran the
-- pre-retrofit shape of 0054 carry this index from then. For those
-- environments this migration is a no-op metadata bump.
--
-- The pricing-rule resolution path uses `properties @> {…}` JSONB subset
-- matches at finalize time over the period's events; GIN over JSONB is
-- the canonical Postgres index for that operator. Without it, every
-- finalize is a sequential scan.

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_usage_events_properties_gin
    ON usage_events USING GIN (properties);
