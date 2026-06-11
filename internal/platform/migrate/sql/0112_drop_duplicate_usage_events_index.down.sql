-- velox:no-transaction
--
-- Recreate the duplicate index (restores the 0001 state). CONCURRENTLY so the
-- rebuild never locks usage_events.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_usage_events_aggregate
    ON usage_events (tenant_id, customer_id, meter_id, "timestamp");
