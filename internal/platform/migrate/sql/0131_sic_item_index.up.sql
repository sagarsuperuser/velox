-- P9: per-ITEM change-history lookups (downgrade-clawback attribution,
-- item timelines) filter subscription_item_changes on
-- subscription_item_id — only (tenant, changed_at) and
-- (subscription_id, changed_at) indexes existed, so per-item reads
-- scanned the subscription's whole history. Small table, plain build.
CREATE INDEX IF NOT EXISTS idx_sic_item
    ON subscription_item_changes (subscription_item_id, changed_at);
