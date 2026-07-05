ALTER TABLE usage_events
    DROP COLUMN IF EXISTS provider_cost_micros,
    DROP COLUMN IF EXISTS provider_cost_source;

DROP TABLE IF EXISTS provider_cost_rates;
