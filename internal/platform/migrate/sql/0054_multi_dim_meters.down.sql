-- Down: revert multi-dim meter foundation.
--
-- Destructive on quantity revert: NUMERIC values with fractional parts will
-- be truncated by the cast to BIGINT. Operators reconciling this in
-- production must verify all live rows hold integer values before applying
-- this down (SELECT count(*) FROM usage_events WHERE quantity != trunc(quantity)).
-- Velox is pre-launch / local-only at this writing, so the down path is
-- exercised only in dev/test environments.

DROP TABLE IF EXISTS meter_pricing_rules;

DROP INDEX IF EXISTS idx_usage_events_properties_gin;

ALTER TABLE usage_events
    ALTER COLUMN quantity TYPE BIGINT USING quantity::bigint;
