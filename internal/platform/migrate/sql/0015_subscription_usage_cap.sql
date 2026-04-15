ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS usage_cap_units BIGINT;
ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS overage_action TEXT NOT NULL DEFAULT 'charge';
