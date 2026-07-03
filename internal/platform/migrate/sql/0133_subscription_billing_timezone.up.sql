-- Snapshot the billing timezone per subscription, paralleling
-- billing_anchor_day (ADR-055). The pair (billing_anchor_day,
-- billing_timezone) immutably defines a subscription's billing calendar,
-- so changing the tenant's timezone setting becomes DISPLAY-only for
-- running subscriptions and only governs newly-created ones — closing the
-- ADR-058 mixed-anchor drift where a settings change silently re-timed
-- live subs.
--
-- Backfill every existing subscription with its tenant's CURRENT timezone
-- so there is ZERO behavior change for in-flight subscriptions: their
-- effective timezone at the moment of this migration becomes their frozen
-- anchor. Rows whose tenant has no timezone configured stay '' and fall
-- back (at read time) to the live tenant timezone, exactly as before.
ALTER TABLE subscriptions ADD COLUMN billing_timezone TEXT;

UPDATE subscriptions s
SET billing_timezone = NULLIF(ts.timezone, '')
FROM tenant_settings ts
WHERE ts.tenant_id = s.tenant_id
  AND s.billing_timezone IS NULL;
