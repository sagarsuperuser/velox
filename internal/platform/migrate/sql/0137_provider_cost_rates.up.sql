-- 0137: provider cost tables + per-event COGS stamp (ADR-079).
--
-- 1) provider_cost_rates — the operator-authoritative table of what THEY
--    pay LLM providers, keyed exactly like pricing dims (provider, model,
--    token_type per ADR-044) so margin joins stay consistent per SKU.
--    CURRENT-rate semantics: one row per key, edited in place — the
--    per-event stamp below is the historical snapshot, so no
--    effective_from versioning (no verified peer effective-dates cost
--    rates; trigger to add = first pre-staged price-change ask).
--    cost_per_token is NUMERIC: verified real-world rates go to
--    1.5e-06 dollars/token — never floats, never cents.
--
-- 2) usage_events cost stamp — cost attaches at INGEST (universal
--    verified pattern: snapshot semantics, non-retroactive). Micros
--    (1e-6 dollars) as BIGINT so aggregate SUMs stay exact; NULL =
--    unresolved (token event, no matching rate — counted honestly) or
--    pre-feature row. provider_cost_source distinguishes 'table'
--    (inferred from this table) from 'not_applicable' (event carries no
--    costable dims — non-token meters) so the honesty counter isn't
--    drowned by structurally-uncostable events. 'observed' (sender-
--    supplied per-half cost) is the named fast-follow; the CHECK admits
--    it now so that leg needs no migration.

CREATE TABLE provider_cost_rates (
    id             TEXT PRIMARY KEY DEFAULT 'vlx_pcr_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id      TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    livemode       BOOLEAN NOT NULL,
    provider       TEXT NOT NULL,
    model          TEXT NOT NULL,
    token_type     TEXT NOT NULL,
    cost_per_token NUMERIC(20, 12) NOT NULL CHECK (cost_per_token >= 0),
    currency       TEXT NOT NULL DEFAULT 'USD',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One current rate per key, per mode.
CREATE UNIQUE INDEX idx_provider_cost_rates_key
    ON provider_cost_rates (tenant_id, livemode, provider, model, token_type);

-- RLS: ENABLE + FORCE + mode-aware tenant_isolation (0006/0020 shape).
ALTER TABLE provider_cost_rates ENABLE ROW LEVEL SECURITY;
ALTER TABLE provider_cost_rates FORCE  ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON provider_cost_rates FOR ALL USING (
    current_setting('app.bypass_rls', true) = 'on'
    OR (
        tenant_id = current_setting('app.tenant_id', true)
        AND livemode = (current_setting('app.livemode', true) IS DISTINCT FROM 'off')
    )
);

-- livemode is session-derived, never caller-supplied (0021 mechanism —
-- the trigger list there is hard-coded, so the new table wires its own).
CREATE TRIGGER set_livemode
    BEFORE INSERT ON provider_cost_rates
    FOR EACH ROW EXECUTE FUNCTION set_livemode_from_session();

ALTER TABLE usage_events
    ADD COLUMN provider_cost_micros BIGINT,
    ADD COLUMN provider_cost_source TEXT
        CONSTRAINT usage_events_cost_source_check
        CHECK (provider_cost_source IN ('table', 'observed', 'not_applicable'));
