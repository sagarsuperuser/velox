-- Restore usage_events.subscription_id.
--
-- Recreates the nullable column and the explicit-RESTRICT FK that
-- existed pre-0079 (originally established by 0015). The column is
-- restored empty — no historical sub linkage is reconstructed,
-- because the column was never populated in the forward direction
-- so there is nothing to back-fill.
ALTER TABLE usage_events ADD COLUMN subscription_id text;
ALTER TABLE usage_events
    ADD CONSTRAINT usage_events_subscription_id_fkey
    FOREIGN KEY (subscription_id) REFERENCES subscriptions(id) ON DELETE RESTRICT NOT VALID;
ALTER TABLE usage_events VALIDATE CONSTRAINT usage_events_subscription_id_fkey;
