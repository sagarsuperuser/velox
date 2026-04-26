-- Stripe-parity billing thresholds. A subscription configures a hard cap
-- (amount_gte cents and/or per-item usage_gte units); when the in-cycle
-- running total crosses, the engine fires an early finalize via
-- billing_reason='threshold' and (when reset_billing_cycle=true) rolls
-- the cycle forward as if the period had ended naturally.
--
-- Distinct from billing alerts (Week 5d) which is the soft warning
-- surface. Thresholds here = hard cap that emits an extra invoice;
-- alerts = email/dashboard notification at percentage of cap.

-- Per-subscription amount cap. NULL on either column = no
-- amount-based threshold configured. CHECK enforces positive cents.
ALTER TABLE subscriptions
    ADD COLUMN billing_threshold_amount_gte BIGINT,
    ADD COLUMN billing_threshold_reset_cycle BOOLEAN NOT NULL DEFAULT TRUE,
    ADD CONSTRAINT subscriptions_billing_threshold_amount_gte_check
        CHECK (billing_threshold_amount_gte IS NULL OR billing_threshold_amount_gte > 0);

-- Per-item usage thresholds. One row per (subscription, item) pair
-- with a quantity threshold. NUMERIC(38,12) keeps decimal precision
-- consistent with the rest of usage rating.
CREATE TABLE subscription_item_thresholds (
    subscription_id        TEXT NOT NULL REFERENCES subscriptions(id) ON DELETE CASCADE,
    subscription_item_id   TEXT NOT NULL REFERENCES subscription_items(id) ON DELETE CASCADE,
    tenant_id              TEXT NOT NULL,
    usage_gte              NUMERIC(38, 12) NOT NULL CHECK (usage_gte > 0),
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (subscription_item_id)
);

CREATE INDEX idx_subscription_item_thresholds_subscription
    ON subscription_item_thresholds (subscription_id);

CREATE INDEX idx_subscription_item_thresholds_tenant
    ON subscription_item_thresholds (tenant_id);

ALTER TABLE subscription_item_thresholds ENABLE ROW LEVEL SECURITY;
ALTER TABLE subscription_item_thresholds FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON subscription_item_thresholds FOR ALL USING (
    current_setting('app.bypass_rls', true) = 'on'
    OR tenant_id = current_setting('app.tenant_id', true)
);

GRANT ALL ON TABLE subscription_item_thresholds TO velox_app;

-- New billing_reason on invoices. Existing rows get NULL (legacy /
-- unspecified); new rows from the cycle scan stamp 'subscription_cycle'
-- and the threshold scan stamps 'threshold'. CHECK constraint allows
-- NULL for legacy rows + the four allowed values.
ALTER TABLE invoices
    ADD COLUMN billing_reason TEXT,
    ADD CONSTRAINT invoices_billing_reason_check
        CHECK (billing_reason IS NULL OR billing_reason IN (
            'subscription_cycle', 'subscription_create', 'manual', 'threshold'
        ));

-- Partial unique index: at most one threshold-fired invoice per
-- (tenant, subscription, billing_period_start) tuple. Idempotency seam
-- for the "tick crossed threshold but advance failed before commit"
-- failure path -- the next tick's INSERT lands on the existing row
-- with errs.ErrAlreadyExists and the engine short-circuits before
-- producing a duplicate invoice. The cycle-natural invoice is unrelated
-- because it carries billing_reason != 'threshold' (or NULL on legacy).
CREATE UNIQUE INDEX idx_invoices_threshold_unique_per_cycle
    ON invoices (tenant_id, subscription_id, billing_period_start)
    WHERE billing_reason = 'threshold';

-- Scan candidate index: subscriptions with an amount threshold set.
-- Per-item thresholds candidate via the aux table's foreign key.
CREATE INDEX idx_subscriptions_billing_thresholds_amount
    ON subscriptions (id)
    WHERE billing_threshold_amount_gte IS NOT NULL;
