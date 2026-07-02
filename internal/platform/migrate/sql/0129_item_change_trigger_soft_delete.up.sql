-- P9 (audit High #13): the 0029 MRR-audit trigger predates 0102's
-- soft delete. Item removal became `UPDATE ... SET deleted_at = now()`,
-- which the trigger's UPDATE branch ignores (it only watches plan_id /
-- quantity) and whose DELETE branch never fires — so every item removal
-- since 0102 wrote NO 'remove' row: MRR contraction silently vanished
-- from analytics, ongoing. Replacement function:
--
--   * soft delete (deleted_at NULL -> NOT NULL) emits 'remove', stamped
--     at the soft-delete instant (honors sim-time stamps);
--   * un-delete (NOT NULL -> NULL) emits 'add' — no current flow does
--     this, but if one ever does, MRR reappearance must not be silent;
--   * mutations of already-deleted rows emit nothing (bookkeeping on
--     dead rows doesn't move MRR);
--   * INSERT / hard-DELETE branches unchanged from 0029.
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
        -- Soft delete IS the remove event post-0102.
        IF NEW.deleted_at IS NOT NULL AND OLD.deleted_at IS NULL THEN
            INSERT INTO subscription_item_changes
                (tenant_id, livemode, subscription_id, subscription_item_id,
                 change_type, from_plan_id, from_quantity, changed_at)
            VALUES
                (OLD.tenant_id, OLD.livemode, OLD.subscription_id, OLD.id,
                 'remove', OLD.plan_id, OLD.quantity, NEW.deleted_at);
            RETURN NEW;
        END IF;
        -- Un-delete: the item's MRR comes back — record it as an 'add'.
        IF NEW.deleted_at IS NULL AND OLD.deleted_at IS NOT NULL THEN
            INSERT INTO subscription_item_changes
                (tenant_id, livemode, subscription_id, subscription_item_id,
                 change_type, to_plan_id, to_quantity, changed_at)
            VALUES
                (NEW.tenant_id, NEW.livemode, NEW.subscription_id, NEW.id,
                 'add', NEW.plan_id, NEW.quantity, NEW.updated_at);
            RETURN NEW;
        END IF;
        -- Updates to rows that are (and stay) soft-deleted don't move MRR.
        IF NEW.deleted_at IS NOT NULL THEN
            RETURN NEW;
        END IF;
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
