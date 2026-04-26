-- velox:no-transaction
--
-- Symmetric rollback. DROP INDEX CONCURRENTLY also cannot run inside a
-- transaction block (Postgres needs to wait for in-flight queries that
-- still see the index), so this down is `velox:no-transaction` too.
-- IF EXISTS so we don't fail rolling back environments where the index
-- was never built (e.g. fresh test DBs that bypassed 0062).

DROP INDEX CONCURRENTLY IF EXISTS idx_usage_events_properties_gin;
