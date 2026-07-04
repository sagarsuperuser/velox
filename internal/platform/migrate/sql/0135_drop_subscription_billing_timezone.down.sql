-- Recreate the column dropped by the up migration (a DROP migration's down
-- must reconstruct the schema it removed) and backfill each subscription
-- with its tenant's CURRENT timezone, exactly as the original 0133 did — so
-- rolling back lands on the ADR-074 shape with the same effective anchors.
ALTER TABLE subscriptions ADD COLUMN billing_timezone TEXT;

UPDATE subscriptions s
SET billing_timezone = NULLIF(ts.timezone, '')
FROM tenant_settings ts
WHERE ts.tenant_id = s.tenant_id
  AND s.billing_timezone IS NULL;
