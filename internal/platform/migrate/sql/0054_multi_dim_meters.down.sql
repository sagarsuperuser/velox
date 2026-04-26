-- Down: revert multi-dim meter foundation.
--
-- Destructive on quantity revert: NUMERIC values with fractional parts will
-- be truncated by the cast to BIGINT. Operators reconciling this in
-- production must verify all live rows hold integer values before applying
-- this down (SELECT count(*) FROM usage_events WHERE quantity != trunc(quantity)).
-- Velox is pre-launch / local-only at this writing, so the down path is
-- exercised only in dev/test environments.

DROP TABLE IF EXISTS meter_pricing_rules;

-- The GIN index originally created here is now owned by migration 0062
-- (CONCURRENTLY) and dropped by 0062's down. Rolling back through 0054
-- with both 0062.down and this 0054.down applied in order leaves the
-- pre-0054 schema, so no cleanup is needed here.

ALTER TABLE usage_events
    ALTER COLUMN quantity TYPE BIGINT USING quantity::bigint;
