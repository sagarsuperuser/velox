-- Scheduled plan changes: instead of swapping plan_id immediately on
-- ChangePlan(immediate=false), record the target plan + effective timestamp
-- so the billing engine can apply it at the cycle boundary. Prior behaviour
-- mutated plan_id on the request which made the API contract a lie — the
-- response reported EffectiveAt=period_end but the plan was already swapped.
--
-- Both columns are nullable. A subscription with a null pending_plan_id has
-- no scheduled change. Applying the change (at cycle boundary) clears both
-- columns in the same transaction that writes the new plan_id.

ALTER TABLE subscriptions
    ADD COLUMN IF NOT EXISTS pending_plan_id TEXT REFERENCES plans(id),
    ADD COLUMN IF NOT EXISTS pending_plan_effective_at TIMESTAMPTZ;

-- Partial index lets the billing engine cheaply find subscriptions with a
-- pending change that's now due. Empty predicate on insert-heavy tables
-- would bloat the index for zero benefit; the partial keeps it narrow.
CREATE INDEX IF NOT EXISTS idx_subscriptions_pending_plan_due
    ON subscriptions (pending_plan_effective_at)
    WHERE pending_plan_id IS NOT NULL;
