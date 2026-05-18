-- Restore 'paused' to the subscriptions.status CHECK enum. Pure-schema
-- rollback — does NOT bring back the Service.Pause / Resume API surface
-- removed in PR-8. If you need the runtime behavior too, revert the
-- accompanying commit(s).

ALTER TABLE subscriptions
    DROP CONSTRAINT IF EXISTS subscriptions_status_check;

ALTER TABLE subscriptions
    ADD CONSTRAINT subscriptions_status_check
        CHECK (status IN ('draft', 'trialing', 'active', 'paused', 'canceled', 'archived'));
