-- Add 'past_due' and 'unpaid' to the subscription status enum.
--
-- past_due: at least one invoice has failed payment and dunning is
--   currently running. Reversible — a successful retry flips the sub
--   back to active. New billing cycles still generate finalized
--   invoices (Stripe parity); the engine keeps trying.
--
-- unpaid: dunning has exhausted all retries. Engine has stopped
--   automatic recovery. New billing cycles generate as DRAFT (not
--   finalized) — audit trail without stacked failures or repeated
--   dunning emails. Reversible only by operator action.
--
-- Both states match Stripe's subscription status grammar so DPs
-- integrating "show me past_due customers" or "revoke API access if
-- past_due" can use a familiar query shape.
--
-- Transitions are wired off existing dunning events (started →
-- past_due, resolved → active, exhausted → unpaid) — no new state
-- machine. See ADR-013 follow-up. Drop-and-recreate is the idiomatic
-- Postgres path for evolving a CHECK enum; safe online because the
-- new constraint is a strict superset.

ALTER TABLE subscriptions
    DROP CONSTRAINT IF EXISTS subscriptions_status_check;

ALTER TABLE subscriptions
    ADD CONSTRAINT subscriptions_status_check
        CHECK (status IN ('draft', 'trialing', 'active', 'past_due', 'unpaid', 'paused', 'canceled', 'archived'));
