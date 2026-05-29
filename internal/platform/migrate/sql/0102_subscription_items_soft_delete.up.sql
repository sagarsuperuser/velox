-- Soft-delete subscription_items so a customer who's been billed via
-- this item (proration invoice, credit ledger entry) can still be
-- removed from the sub without the FK
-- (invoices_source_subscription_item_id_fkey,
-- customer_credit_ledger_source_subscription_item_id_fkey) blocking
-- the operation.
--
-- Industry standard: Stripe / Chargebee / Lago / Orb all soft-delete
-- subscription items — the row stays in the DB for historical billing
-- reference (the FK back-pointer is preserved), and a deleted_at
-- column makes the item invisible to the active subscription's Items
-- slice + every billing-cycle query.
--
-- Surfaced 2026-05-29: EX3 manual test ran DELETE on a sub item that
-- had a `add`-type proration invoice (DEMO-000938) — FK violation 500
-- to the operator with a raw Postgres error in the logs.

-- IF NOT EXISTS keeps the migration idempotent — re-running on a DB
-- that already has the column (e.g. manual psql apply during local
-- iteration) skips the ADD instead of failing the boot.
ALTER TABLE subscription_items
    ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ NULL;

-- The existing UNIQUE (subscription_id, plan_id) constraint must
-- become partial: only LIVE items collide on the (sub, plan) pair.
-- A soft-deleted item with plan_id=p shouldn't block re-adding plan p
-- to the same sub.
--
-- DROP CONSTRAINT must come before DROP INDEX because the constraint
-- owns the index; trying to drop the index first errors with "cannot
-- drop because constraint requires it".
ALTER TABLE subscription_items
    DROP CONSTRAINT IF EXISTS subscription_items_subscription_id_plan_id_key;
DROP INDEX IF EXISTS subscription_items_subscription_id_plan_id_key;
CREATE UNIQUE INDEX IF NOT EXISTS subscription_items_subscription_id_plan_id_key
    ON subscription_items (subscription_id, plan_id)
    WHERE deleted_at IS NULL;

-- Hot-path index: every "current state" query filters on
-- deleted_at IS NULL. Without this the planner falls back to a seq
-- scan on subs with many historical item versions.
CREATE INDEX IF NOT EXISTS idx_subscription_items_live
    ON subscription_items (subscription_id)
    WHERE deleted_at IS NULL;
