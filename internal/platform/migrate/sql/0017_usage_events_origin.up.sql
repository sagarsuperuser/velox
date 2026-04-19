-- FEAT-7: distinguish real-time API ingest from historical backfills.
--
-- The /usage-events POST endpoint accepts events as they happen; the new
-- /usage-events/backfill endpoint accepts events with past timestamps (CSV
-- import, historical reconciliation, migration from another billing system).
-- Tagging the row with the ingest source lets operators audit what entered
-- the ledger and via which path — and lets future features filter backfill
-- rows out of live-revenue metrics without needing to reason about timestamps.
--
-- Default 'api' keeps the invariant: any existing row pre-dating this column
-- is, by definition, real-time ingest.

ALTER TABLE usage_events
    ADD COLUMN origin TEXT NOT NULL DEFAULT 'api'
        CHECK (origin IN ('api', 'backfill'));

-- No index on origin: backfill is a minority of traffic, and the aggregation
-- query does not filter by origin (backfilled events are billable in future
-- cycles same as real-time events). An index here would cost writes without
-- benefit until a future operator-UI filter query needs it.
