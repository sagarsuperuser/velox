-- Restore the pre-0129 (0029) trigger function: soft deletes go back
-- to emitting no 'remove' row. Only sensible as part of rolling 0129
-- back entirely.
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
