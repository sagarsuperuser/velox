-- Multi-dimensional meters: foundation for AI-native pricing.
--
-- Three coordinated changes per docs/design-multi-dim-meters.md:
--
--   1. usage_events.quantity becomes NUMERIC(38, 12).
--      AI usage is intrinsically fractional (GPU-hours, cached-token
--      ratios, partial KV-cache reads) and BIGINT can only express it via
--      lossy unit-scaling at the application layer. NUMERIC(38, 12) gives
--      the operator a quantity primitive that holds both whole counts and
--      fractions without a per-tenant scale convention. Maps to Stripe's
--      quantity_decimal (Tier 1 parity gap).
--
--   2. GIN index on usage_events.properties.
--      Pricing-rule resolution is a JSONB subset-match (`properties @> {...}`)
--      executed at finalize time across the period's events. GIN over JSONB
--      is the canonical Postgres index for this; without it, every finalize
--      becomes a sequential scan over the period.
--
--   3. New meter_pricing_rules table. N rules per meter, each carrying
--      a dimension_match JSONB filter, an aggregation_mode, and a priority.
--      Each event is claimed by the highest-priority matching rule (no
--      double-count). Default rule has dimension_match='{}' which matches
--      everything; priority=0 keeps it last so specific rules win first.
--
-- Backward compatibility:
--   - meters.aggregation column is left in place but becomes advisory; the
--     authoritative aggregation mode is now per-rule on meter_pricing_rules.
--     A separate cleanup migration may deprecate it once all consumers move.
--   - quantity column rename: BIGINT -> NUMERIC is a metadata-only change
--     when no rows exist (pre-launch), and a rewrite when rows exist. We
--     are pre-launch / local-only, so this is effectively free.

ALTER TABLE usage_events
    ALTER COLUMN quantity TYPE NUMERIC(38, 12) USING quantity::numeric;

CREATE INDEX idx_usage_events_properties_gin
    ON usage_events USING GIN (properties);

CREATE TABLE meter_pricing_rules (
    id                      TEXT PRIMARY KEY DEFAULT 'vlx_mpr_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id               TEXT NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
    meter_id                TEXT NOT NULL REFERENCES meters(id) ON DELETE CASCADE,
    rating_rule_version_id  TEXT NOT NULL REFERENCES rating_rule_versions(id) ON DELETE RESTRICT,
    dimension_match         JSONB NOT NULL DEFAULT '{}'::jsonb,
    aggregation_mode        TEXT NOT NULL DEFAULT 'sum'
                                CHECK (aggregation_mode IN ('sum', 'count', 'last_during_period', 'last_ever', 'max')),
    priority                INT NOT NULL DEFAULT 0,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, meter_id, rating_rule_version_id)
);

-- Hot path: rule resolution iterates rules for a meter in priority order
-- (highest first; default rule with priority=0 is last). The descending
-- index sort matches the runtime sort and avoids in-memory ordering.
CREATE INDEX idx_meter_pricing_rules_lookup
    ON meter_pricing_rules (tenant_id, meter_id, priority DESC);

-- Standard tenant-isolation pattern (see 0046 for prior art). FORCE makes
-- the policy apply even to the table owner, so a misconfigured connection
-- string can't accidentally bypass it.
ALTER TABLE meter_pricing_rules ENABLE ROW LEVEL SECURITY;
ALTER TABLE meter_pricing_rules FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON meter_pricing_rules FOR ALL USING (
    current_setting('app.bypass_rls', true) = 'on'
    OR tenant_id = current_setting('app.tenant_id', true)
);

GRANT ALL ON TABLE meter_pricing_rules TO velox_app;
