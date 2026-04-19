-- A customer must not hold two simultaneously-live subscriptions on the
-- same plan. The (tenant_id, code) UNIQUE that already exists prevents
-- duplicate *codes*, but nothing in the schema stops a caller from
-- issuing `sub-a` and `sub-b` both pointing at (customer_X, plan_Y) with
-- status=active — double-billing the customer every cycle.
--
-- Partial UNIQUE over the live-status set closes that hole at the DB
-- level. The plan doc proposed `status IN ('active','trialing','past_due')`
-- following Stripe's vocabulary, but Velox's actual status set is
-- {draft,active,paused,canceled,archived} (see internal/domain/subscription.go).
-- Using the real values:
--   * active: obvious — the billing-cycle owner
--   * paused: still "owned" by this (customer, plan) pair, just dormant;
--     allowing a new active while one is paused would orphan the paused row
--   * draft: excluded. Draft is pre-commit configuration state — multiple
--     parallel drafts during UI editing must remain legal
--   * canceled/archived: terminal, the (customer, plan) pair is free to
--     be re-subscribed later
--
-- Using a partial INDEX rather than a table-level constraint because
-- Postgres only supports partial predicates on indexes (CHECK constraints
-- cannot reference WHERE).
CREATE UNIQUE INDEX subscriptions_one_live_per_customer_plan
    ON subscriptions (tenant_id, customer_id, plan_id)
    WHERE status IN ('active', 'paused');
