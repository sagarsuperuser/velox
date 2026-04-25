-- Down: revert to the original subscription status set. This is destructive
-- if any rows were stamped 'trialing' before the down — those rows will fail
-- the new check. Operator must reconcile (UPDATE ... SET status='active'
-- WHERE status='trialing') before applying down in production. The migration
-- runner does not auto-reconcile.

ALTER TABLE subscriptions
    DROP CONSTRAINT IF EXISTS subscriptions_status_check;

ALTER TABLE subscriptions
    ADD CONSTRAINT subscriptions_status_check
        CHECK (status IN ('draft', 'active', 'paused', 'canceled', 'archived'));
