-- velox:no-transaction
-- P9: tenant-wide time-window analytics (dashboard overview, usage
-- charts) filter usage_events on (tenant_id, timestamp) with no
-- customer/meter prefix — the existing composite index leads with
-- customer_id+meter_id, so those queries seq-scanned the whole events
-- table. CONCURRENTLY: usage_events is the highest-write table in the
-- product; a blocking index build would stall ingestion.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_usage_events_tenant_time
    ON usage_events (tenant_id, timestamp);
