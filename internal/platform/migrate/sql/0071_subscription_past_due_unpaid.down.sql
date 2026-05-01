-- Revert the past_due/unpaid additions. Any rows in those states need
-- to be flipped to a valid pre-migration status first; the down
-- migration is destructive on uncategorised rows.

UPDATE subscriptions SET status = 'active' WHERE status IN ('past_due', 'unpaid');

ALTER TABLE subscriptions
    DROP CONSTRAINT IF EXISTS subscriptions_status_check;

ALTER TABLE subscriptions
    ADD CONSTRAINT subscriptions_status_check
        CHECK (status IN ('draft', 'trialing', 'active', 'paused', 'canceled', 'archived'));
