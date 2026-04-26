-- Billing alerts: operator-configured thresholds that fire a webhook +
-- dashboard notification when a customer's cycle spend crosses a limit.
-- Stripe-equivalent "Billing Alerts" Tier 1 surface.
--
-- Two tables:
--
--   billing_alerts          — the alert configuration (one row per alert)
--   billing_alert_triggers  — one row per fire event, with UNIQUE
--                             (alert_id, period_from) for idempotency
--                             across evaluator retries / replica races
--
-- See docs/design-billing-alerts.md for the full design including
-- recurrence semantics, atomicity contract, and edge cases.

CREATE TABLE billing_alerts (
    id                     TEXT PRIMARY KEY DEFAULT 'vlx_alrt_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id              TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    -- Mode partition. Defaulting to live keeps any future un-mode-aware
    -- INSERT path safe — a missing app.livemode session var routes to
    -- live by the RLS policy, matching every other mode-aware table.
    livemode               BOOLEAN NOT NULL DEFAULT true,
    customer_id            TEXT NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    title                  TEXT NOT NULL,
    -- Optional meter filter. ON DELETE SET NULL so that deleting the
    -- meter doesn't cascade-delete alerts the operator may want to
    -- repurpose; the evaluator surfaces the missing-meter case as a
    -- warning and the operator can edit or archive.
    meter_id               TEXT REFERENCES meters(id) ON DELETE SET NULL,
    -- Dimension filter for multi-dim meters. JSONB '{}' is the always-
    -- object identity (no filter); a non-empty object is a strict-
    -- superset match against rule dimension_match.
    dimensions             JSONB NOT NULL DEFAULT '{}'::jsonb,
    -- Exactly one of these two threshold columns is set; the CHECK
    -- below enforces it. Quantity is NUMERIC(38,12) to match the
    -- usage_events.quantity precision; amount is BIGINT cents.
    threshold_amount_cents BIGINT,
    threshold_quantity     NUMERIC(38,12),
    recurrence             TEXT NOT NULL CHECK (recurrence IN ('one_time','per_period')),
    status                 TEXT NOT NULL DEFAULT 'active'
                                CHECK (status IN ('active','triggered','triggered_for_period','archived')),
    last_triggered_at      TIMESTAMPTZ,
    last_period_start      TIMESTAMPTZ,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Exactly one threshold field set. Adding both or neither is a
    -- caller error that the service-layer validation also catches; the
    -- DB CHECK is the safety net that prevents a malformed migration
    -- backfill or admin INSERT from creating an unfireable row.
    CHECK ( (threshold_amount_cents IS NOT NULL)::int + (threshold_quantity IS NOT NULL)::int = 1 )
);

-- Hot read: dashboard list + filter alerts by customer + status.
CREATE INDEX idx_billing_alerts_tenant_customer
    ON billing_alerts (tenant_id, customer_id, status);

-- Evaluator hot scan: only candidate-firing rows. Excludes archived and
-- already-triggered one_time rows by predicate so the evaluator's per-
-- tick scan stays bounded by the count of currently-armed alerts.
CREATE INDEX idx_billing_alerts_evaluator
    ON billing_alerts (status)
    WHERE status IN ('active','triggered_for_period');

-- Standard tenant + mode isolation. FORCE applies the policy even to
-- the table owner so a misconfigured connection string can't bypass it.
-- Mode predicate matches the project-wide convention from migration
-- 0020_test_mode: livemode unset/'on' → live, 'off' → test.
ALTER TABLE billing_alerts ENABLE ROW LEVEL SECURITY;
ALTER TABLE billing_alerts FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON billing_alerts FOR ALL USING (
    current_setting('app.bypass_rls', true) = 'on'
    OR (
        tenant_id = current_setting('app.tenant_id', true)
        AND livemode = (current_setting('app.livemode', true) IS DISTINCT FROM 'off')
    )
);

GRANT ALL ON TABLE billing_alerts TO velox_app;

CREATE TABLE billing_alert_triggers (
    id                    TEXT PRIMARY KEY DEFAULT 'vlx_atrg_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id             TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    livemode              BOOLEAN NOT NULL DEFAULT true,
    alert_id              TEXT NOT NULL REFERENCES billing_alerts(id) ON DELETE CASCADE,
    period_from           TIMESTAMPTZ NOT NULL,
    period_to             TIMESTAMPTZ NOT NULL,
    observed_amount_cents BIGINT NOT NULL,
    observed_quantity     NUMERIC(38,12) NOT NULL DEFAULT 0,
    currency              TEXT NOT NULL,
    triggered_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Per-period idempotency. Two replicas racing the evaluator can
    -- both attempt to insert; one wins, the other gets a unique-
    -- violation that rolls its entire tx back (status update + outbox
    -- enqueue) so no double-emission.
    UNIQUE (alert_id, period_from)
);

-- Hot read: dashboard "trigger history for this alert".
CREATE INDEX idx_billing_alert_triggers_alert
    ON billing_alert_triggers (alert_id, triggered_at DESC);

ALTER TABLE billing_alert_triggers ENABLE ROW LEVEL SECURITY;
ALTER TABLE billing_alert_triggers FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON billing_alert_triggers FOR ALL USING (
    current_setting('app.bypass_rls', true) = 'on'
    OR (
        tenant_id = current_setting('app.tenant_id', true)
        AND livemode = (current_setting('app.livemode', true) IS DISTINCT FROM 'off')
    )
);

GRANT ALL ON TABLE billing_alert_triggers TO velox_app;

-- Wire the BEFORE INSERT livemode trigger from migration 0021 onto both
-- new mode-aware tables. This makes the row's livemode column track the
-- app.livemode session var instead of the column DEFAULT, so a TxTenant
-- under test-mode session correctly lands rows with livemode=false even
-- when the INSERT statement omits the column. Same pattern as every
-- mode-aware table created after migration 0021.
CREATE TRIGGER set_livemode
    BEFORE INSERT ON billing_alerts
    FOR EACH ROW EXECUTE FUNCTION set_livemode_from_session();

CREATE TRIGGER set_livemode
    BEFORE INSERT ON billing_alert_triggers
    FOR EACH ROW EXECUTE FUNCTION set_livemode_from_session();
