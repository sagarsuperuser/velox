-- Scheduled subscription cancellation (Stripe-parity).
--
-- Two cancel intents distinct from the immediate cancel POST /cancel path:
--
--   cancel_at_period_end — soft, reversible. Set true and the next billing
--   cycle scan flips the subscription to canceled at current_period_end
--   instead of generating the next invoice. Set false to undo before the
--   boundary fires; idempotent.
--
--   cancel_at — explicit timestamp. The cycle scan compares against
--   effectiveNow (test-clock-aware) and fires when due. v1 expects callers
--   to pass a timestamp >= current_period_end so the current period bills
--   normally and the cancel lands on a clean boundary; the
--   shorten-current-period + prorate variant is a follow-up.
--
-- The two are mutually-permissive at the row level (a row can carry both,
-- whichever fires first wins) but the service layer rejects "schedule both
-- in the same call" to keep caller intent unambiguous.
--
-- Partial index on cancel_at WHERE NOT NULL keeps the future-due lookup
-- cheap once enough subs accumulate; the existing GetDueBilling already
-- pulls subs by next_billing_at so the cycle path doesn't need a separate
-- scan, but the index supports admin queries and forthcoming
-- "scheduled-cancel" lists in the dashboard.
ALTER TABLE subscriptions ADD COLUMN cancel_at_period_end BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE subscriptions ADD COLUMN cancel_at TIMESTAMPTZ;
CREATE INDEX idx_subscriptions_cancel_at ON subscriptions (cancel_at) WHERE cancel_at IS NOT NULL;
