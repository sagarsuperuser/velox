-- Customer-level test-clock attach (ADR-027).
--
-- Velox previously attached test clocks at the subscription level
-- (subscriptions.test_clock_id), allowing a single customer to have
-- some subs on a test clock and others on wall-clock. This was more
-- flexible than Stripe's design but introduced inconsistency:
-- per-customer state (credit balance, payment-method setup, dunning
-- override, billing profile) is consumed by subs but those subs
-- could be running on different clocks, so "now" was ambiguous at
-- the customer level. Tax calculation timing, credit-grant
-- expiration, and dunning-retry scheduling all assume a single
-- coherent clock per customer.
--
-- Stripe's API attaches test clocks at customer-create time; once
-- set, every Subscription / Invoice / PaymentIntent for that
-- customer runs on that clock's time. All-or-nothing per customer.
-- ADR-027 adopts that model.
--
-- This migration:
--   1. Adds customers.test_clock_id (nullable, FK to test_clocks
--      with ON DELETE SET NULL — same shape as the existing
--      subscriptions.test_clock_id).
--   2. Backfills from any existing subscription-level pinning. If a
--      customer has multiple subs with different test_clock_ids
--      (rare but possible under the old model), the FIRST non-null
--      test_clock_id encountered wins; the others remain on the sub
--      column and will be reconciled by the application layer (sub
--      service refuses creates that don't match the customer's
--      clock; existing mismatched subs are surfaced via a follow-up
--      audit query).
--   3. Indexes on (tenant_id, test_clock_id) for the
--      "list-customers-attached-to-clock" UI surface (ADR-027 Tier 3,
--      replicating Stripe's test-clock detail page).
--
-- subscriptions.test_clock_id is intentionally NOT removed in this
-- migration. It becomes a denormalized cache of the customer's
-- value; the application layer enforces sub.test_clock_id ==
-- customer.test_clock_id at create time. Keeping the column avoids
-- engine-query rewrites and lets the deprecation be a separate
-- migration once all writers are confirmed customer-driven.

ALTER TABLE customers
  ADD COLUMN test_clock_id TEXT
    REFERENCES test_clocks(id) ON DELETE SET NULL;

-- Backfill: pick a customer's test clock from its subs, if any.
-- DISTINCT ON gives us the first one per customer (ordered by
-- created_at ASC for determinism; we pick the OLDEST sub's clock
-- because that's the longest-running simulation context, most
-- likely the canonical one if there's any disagreement).
UPDATE customers c
SET test_clock_id = sub.test_clock_id
FROM (
  SELECT DISTINCT ON (customer_id) customer_id, test_clock_id
  FROM subscriptions
  WHERE test_clock_id IS NOT NULL
  ORDER BY customer_id, created_at ASC
) sub
WHERE c.id = sub.customer_id;

-- Reconcile: under the old per-sub model a single customer could
-- (in principle) have subs on different clocks. The backfill above
-- picks ONE clock for the customer; this UPDATE forces every sub
-- to match the customer's chosen clock, restoring the post-fix
-- invariant (sub.test_clock_id == customer.test_clock_id).
--
-- IS DISTINCT FROM treats NULL ≠ X as a real difference (unlike
-- bare !=), so customer-with-clock + sub-with-NULL is also caught
-- and reconciled. If both are NULL it's a no-op.
--
-- Behavioural impact: a sub previously simulating on clock Y where
-- the customer ended up pinned to clock X will now simulate on X
-- instead. For pre-launch dev data this is the right call — the
-- alternative is leaving the inconsistency in place and hoping the
-- operator notices. For a future production migration this step
-- would be opted into separately; today we just do it.
UPDATE subscriptions s
SET test_clock_id = c.test_clock_id, updated_at = now()
FROM customers c
WHERE s.customer_id = c.id
  AND s.test_clock_id IS DISTINCT FROM c.test_clock_id;

-- Index for Tier 3 "list customers attached to this clock" — the
-- test-clock detail page replicates Stripe's "attached customers"
-- list. Without this, the lookup scans all customers in the tenant.
-- Partial: only customers actually attached.
CREATE INDEX idx_customers_test_clock
  ON customers (tenant_id, test_clock_id)
  WHERE test_clock_id IS NOT NULL;
