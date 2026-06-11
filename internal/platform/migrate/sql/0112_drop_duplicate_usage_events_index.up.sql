-- velox:no-transaction
--
-- Drop the duplicate index on usage_events. idx_usage_events_aggregate and
-- idx_usage_events_customer_meter are byte-identical btrees over
-- (tenant_id, customer_id, meter_id, timestamp) — both created in 0001. The
-- planner can only ever use one; the other is pure write amplification on the
-- highest-write table in the engine (every usage-event ingest maintains both).
-- CONCURRENTLY (no-tx) so the drop never holds an AccessExclusiveLock on
-- usage_events — see 0062 for the same posture on this table.
DROP INDEX CONCURRENTLY IF EXISTS idx_usage_events_aggregate;
