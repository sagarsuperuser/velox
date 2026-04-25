-- Add 'trialing' as a valid subscription status. Stripe-parity: a subscription
-- with an active trial is its own state, distinct from the post-trial 'active'
-- state. This lets the dashboard, webhooks, and analytics distinguish "in
-- trial" from "billing normally" without inferring it from the trial_end_at
-- timestamp.
--
-- Drop-and-recreate is the idiomatic Postgres path for evolving a CHECK
-- constraint enum (no native ALTER for this). Safe online: no row scan
-- required because the new constraint is a strict superset of the old.

ALTER TABLE subscriptions
    DROP CONSTRAINT IF EXISTS subscriptions_status_check;

ALTER TABLE subscriptions
    ADD CONSTRAINT subscriptions_status_check
        CHECK (status IN ('draft', 'trialing', 'active', 'paused', 'canceled', 'archived'));
