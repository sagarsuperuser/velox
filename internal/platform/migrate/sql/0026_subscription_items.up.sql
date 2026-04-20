-- FEAT-5: multi-item subscriptions. A subscription becomes a container for N
-- priced items (plan + quantity), replacing the prior one-plan-per-subscription
-- model. Each item owns its own pending-plan-change state so per-item upgrades
-- and downgrades can schedule independently (Stripe-equivalent semantics).
--
-- Pre-launch clean break: `subscriptions.plan_id` and its companions
-- (previous_plan_id, plan_changed_at, pending_plan_id, pending_plan_effective_at)
-- are dropped outright. Down-migration recreates the columns but does not
-- reconstruct a value from items — restoring a single plan_id from N items is
-- ambiguous and would silently pick the wrong one. Any caller that needs the
-- old shape must re-run the app's seed path.

-- ===========================================================================
-- 1. subscription_items table
-- ===========================================================================
CREATE TABLE subscription_items (
    id                         TEXT PRIMARY KEY DEFAULT 'vlx_si_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id                  TEXT NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
    livemode                   BOOLEAN NOT NULL DEFAULT true,
    subscription_id            TEXT NOT NULL REFERENCES subscriptions(id) ON DELETE CASCADE,
    plan_id                    TEXT NOT NULL REFERENCES plans(id) ON DELETE RESTRICT,
    quantity                   BIGINT NOT NULL DEFAULT 1 CHECK (quantity >= 1),
    metadata                   JSONB NOT NULL DEFAULT '{}',
    pending_plan_id            TEXT REFERENCES plans(id) ON DELETE RESTRICT,
    pending_plan_effective_at  TIMESTAMPTZ,
    -- plan_changed_at stamps the last immediate plan swap on this item. It
    -- feeds the proration dedup key (see invoices.source_plan_changed_at +
    -- idx_invoices_proration_dedup) so retries of the same change resolve to
    -- the existing invoice rather than writing a duplicate. Quantity changes
    -- do not touch it — quantity proration lands on a different code path
    -- that doesn't share this key.
    plan_changed_at            TIMESTAMPTZ,
    created_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- One occurrence per plan within a subscription. Quantity changes mutate
    -- the row in place; a second row with the same (subscription, plan) would
    -- scatter aggregation for billing and muddy coupon plan_id matching.
    UNIQUE (subscription_id, plan_id),

    -- Pending-change column pair must be all-or-nothing. A dangling
    -- pending_plan_id with no effective_at ("apply when?") or vice versa
    -- ("apply what?") would leave the cycle-boundary apply path guessing.
    CONSTRAINT subscription_items_pending_both_or_neither CHECK (
        (pending_plan_id IS NULL AND pending_plan_effective_at IS NULL)
        OR (pending_plan_id IS NOT NULL AND pending_plan_effective_at IS NOT NULL)
    )
);

-- Hot path: load all items for a given subscription (handler, billing engine,
-- proration compute).
CREATE INDEX idx_subscription_items_subscription
    ON subscription_items (subscription_id);

-- Secondary: coupon plan_ids gating enumerates item plan_ids per subscription;
-- operator queries like "which subscriptions reference plan X" hit this too.
CREATE INDEX idx_subscription_items_plan
    ON subscription_items (tenant_id, livemode, plan_id);

-- Per-item pending-change due index — replaces subscriptions.
-- idx_subscriptions_pending_plan_due. The billing engine's cycle-boundary
-- apply path scans this to find items whose scheduled change has come due.
CREATE INDEX idx_subscription_items_pending_due
    ON subscription_items (pending_plan_effective_at)
    WHERE pending_plan_id IS NOT NULL;

-- RLS: tenant isolation with livemode partition, matching the 0020 pattern.
-- Bypass remains the escape hatch for TxBypass (migrations, platform admin).
ALTER TABLE subscription_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE subscription_items FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON subscription_items FOR ALL USING (
    current_setting('app.bypass_rls', true) = 'on'
    OR (tenant_id = current_setting('app.tenant_id', true)
        AND livemode = (current_setting('app.livemode', true) IS DISTINCT FROM 'off'))
);

-- Livemode auto-set trigger: without it an INSERT under app.livemode='off'
-- would write livemode=true (column default) and fail RLS WITH CHECK against
-- the session's 'off'. Mirrors the per-table trigger installed in 0021.
CREATE TRIGGER set_livemode
    BEFORE INSERT ON subscription_items
    FOR EACH ROW EXECUTE FUNCTION set_livemode_from_session();

GRANT ALL ON TABLE subscription_items TO velox_app;

-- ===========================================================================
-- 2. Extend proration dedup keys for per-item changes
-- ===========================================================================
-- Before FEAT-5 the natural idempotency key for a plan-change-derived invoice
-- was (tenant, subscription, plan_changed_at). With items, two items can
-- change in the same transaction and share the same wall-clock timestamp —
-- the key must include the item id to keep them distinct.

ALTER TABLE invoices
    ADD COLUMN source_subscription_item_id TEXT REFERENCES subscription_items(id) ON DELETE RESTRICT;

DROP INDEX idx_invoices_proration_dedup;
CREATE UNIQUE INDEX idx_invoices_proration_dedup
    ON invoices (tenant_id, subscription_id, source_subscription_item_id, source_plan_changed_at)
    WHERE source_plan_changed_at IS NOT NULL;

ALTER TABLE customer_credit_ledger
    ADD COLUMN source_subscription_item_id TEXT REFERENCES subscription_items(id) ON DELETE RESTRICT;

DROP INDEX idx_credit_ledger_proration_dedup;
CREATE UNIQUE INDEX idx_credit_ledger_proration_dedup
    ON customer_credit_ledger (tenant_id, source_subscription_id, source_subscription_item_id, source_plan_changed_at)
    WHERE source_subscription_id IS NOT NULL AND source_plan_changed_at IS NOT NULL;

-- ===========================================================================
-- 3. Drop plan-scoped indexes on subscriptions (obsolete once plan_id leaves)
-- ===========================================================================
-- HYG-2's partial UNIQUE prevented duplicate live subscriptions on the same
-- (customer, plan). With plan_id gone from subscriptions, "duplicate plan
-- within a subscription" is prevented by UNIQUE (subscription_id, plan_id)
-- on items. Cross-subscription plan uniqueness per customer is left to
-- application policy (matches Stripe: a customer can hold multiple active
-- subscriptions that happen to reference the same plan via different items).
DROP INDEX IF EXISTS subscriptions_one_live_per_customer_plan;

-- Moves to idx_subscription_items_pending_due.
DROP INDEX IF EXISTS idx_subscriptions_pending_plan_due;

-- ===========================================================================
-- 4. Drop plan-scoped columns from subscriptions
-- ===========================================================================
-- FK constraints referencing plans go with the columns. No data preserved —
-- the app layer will seed items from CreateInput in the rewritten service.
ALTER TABLE subscriptions
    DROP COLUMN plan_id,
    DROP COLUMN previous_plan_id,
    DROP COLUMN plan_changed_at,
    DROP COLUMN pending_plan_id,
    DROP COLUMN pending_plan_effective_at;
