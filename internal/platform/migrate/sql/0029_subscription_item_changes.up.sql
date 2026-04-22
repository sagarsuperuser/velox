-- MRR-movement audit: every mutation to subscription_items that changes MRR
-- emits a row here. Populated by a DB trigger so no Go call site can bypass
-- it, and so the audit write shares the same transaction as the mutation.
--
-- Motivation: FEAT-5 (0026) dropped subscriptions.previous_plan_id — the
-- column analytics relied on to compute expansion/contraction from plan
-- swaps. With plans + quantity living per-item, and a subscription able to
-- hold N items, only a full event log captures the before/after needed to
-- classify MRR movement.
--
-- Clean break: this codebase has never shipped, so we don't backfill from
-- existing data. Fresh databases start with an empty change log; existing
-- subscriptions' current items stand as-is (no synthetic 'add' events
-- inserted retroactively).

-- ===========================================================================
-- 1. Audit table
-- ===========================================================================
CREATE TABLE subscription_item_changes (
    id                   TEXT PRIMARY KEY DEFAULT 'vlx_sic_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id            TEXT NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
    livemode             BOOLEAN NOT NULL DEFAULT true,
    subscription_id      TEXT NOT NULL REFERENCES subscriptions(id) ON DELETE CASCADE,
    -- Nullable: the originating item may be deleted after a 'remove' event.
    -- No FK for the same reason — retaining history outweighs referential
    -- tightness here.
    subscription_item_id TEXT,
    -- Vocabulary matches invoices.source_change_type (migration 0027) so a
    -- proration invoice can be joined back to the change that produced it.
    change_type          TEXT NOT NULL CHECK (change_type IN ('add', 'remove', 'plan', 'quantity')),
    from_plan_id         TEXT REFERENCES plans(id) ON DELETE RESTRICT,
    to_plan_id           TEXT REFERENCES plans(id) ON DELETE RESTRICT,
    from_quantity        BIGINT,
    to_quantity          BIGINT,
    changed_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Analytics hot path: SUM MRR deltas in a time window, tenant-scoped.
CREATE INDEX idx_sic_tenant_changed
    ON subscription_item_changes (tenant_id, livemode, changed_at);

-- Per-subscription timeline (history view, churn attribution).
CREATE INDEX idx_sic_subscription
    ON subscription_item_changes (subscription_id, changed_at);

-- ===========================================================================
-- 2. RLS — same pattern as subscription_items
-- ===========================================================================
ALTER TABLE subscription_item_changes ENABLE ROW LEVEL SECURITY;
ALTER TABLE subscription_item_changes FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON subscription_item_changes FOR ALL USING (
    current_setting('app.bypass_rls', true) = 'on'
    OR (tenant_id = current_setting('app.tenant_id', true)
        AND livemode = (current_setting('app.livemode', true) IS DISTINCT FROM 'off'))
);

GRANT ALL ON TABLE subscription_item_changes TO velox_app;

-- ===========================================================================
-- 3. Audit trigger on subscription_items
-- ===========================================================================
-- Emits exactly one audit row per MRR-relevant mutation. Bookkeeping-only
-- updates (metadata-only changes, pending_plan_id scheduling, updated_at
-- touches) produce no row — those don't move MRR.
CREATE OR REPLACE FUNCTION record_subscription_item_change() RETURNS TRIGGER AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        INSERT INTO subscription_item_changes
            (tenant_id, livemode, subscription_id, subscription_item_id,
             change_type, to_plan_id, to_quantity, changed_at)
        VALUES
            (NEW.tenant_id, NEW.livemode, NEW.subscription_id, NEW.id,
             'add', NEW.plan_id, NEW.quantity, NEW.created_at);
        RETURN NEW;

    ELSIF TG_OP = 'UPDATE' THEN
        -- Plan change dominates: if plan_id changed (with or without a quantity
        -- change), emit 'plan' and capture both before/after for a full delta.
        -- A pure quantity change (plan_id unchanged) emits 'quantity'.
        IF NEW.plan_id IS DISTINCT FROM OLD.plan_id THEN
            INSERT INTO subscription_item_changes
                (tenant_id, livemode, subscription_id, subscription_item_id,
                 change_type, from_plan_id, to_plan_id,
                 from_quantity, to_quantity, changed_at)
            VALUES
                (NEW.tenant_id, NEW.livemode, NEW.subscription_id, NEW.id,
                 'plan', OLD.plan_id, NEW.plan_id,
                 OLD.quantity, NEW.quantity,
                 COALESCE(NEW.plan_changed_at, NEW.updated_at));
        ELSIF NEW.quantity IS DISTINCT FROM OLD.quantity THEN
            INSERT INTO subscription_item_changes
                (tenant_id, livemode, subscription_id, subscription_item_id,
                 change_type, from_plan_id, to_plan_id,
                 from_quantity, to_quantity, changed_at)
            VALUES
                (NEW.tenant_id, NEW.livemode, NEW.subscription_id, NEW.id,
                 'quantity', NEW.plan_id, NEW.plan_id,
                 OLD.quantity, NEW.quantity, NEW.updated_at);
        END IF;
        -- Pending-plan scheduling, metadata-only updates: no audit row.
        RETURN NEW;

    ELSIF TG_OP = 'DELETE' THEN
        -- Skip the audit row when the parent subscription itself is being
        -- deleted in the same statement (cascade from DELETE FROM subscriptions).
        -- subscription_id has ON DELETE CASCADE, so the trigger's INSERT would
        -- reference a row that Postgres is about to remove — yielding a
        -- transient FK violation. The history row would also be cascade-deleted
        -- immediately, so recording it first is pointless and breaks legitimate
        -- parent deletion.
        IF NOT EXISTS (SELECT 1 FROM subscriptions WHERE id = OLD.subscription_id) THEN
            RETURN OLD;
        END IF;
        INSERT INTO subscription_item_changes
            (tenant_id, livemode, subscription_id, subscription_item_id,
             change_type, from_plan_id, from_quantity, changed_at)
        VALUES
            (OLD.tenant_id, OLD.livemode, OLD.subscription_id, OLD.id,
             'remove', OLD.plan_id, OLD.quantity, now());
        RETURN OLD;
    END IF;

    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER record_subscription_item_changes
    AFTER INSERT OR UPDATE OR DELETE ON subscription_items
    FOR EACH ROW EXECUTE FUNCTION record_subscription_item_change();
