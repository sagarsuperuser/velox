DROP INDEX IF EXISTS idx_subscriptions_billing_thresholds_amount;
DROP INDEX IF EXISTS idx_invoices_threshold_unique_per_cycle;
ALTER TABLE invoices
    DROP CONSTRAINT IF EXISTS invoices_billing_reason_check,
    DROP COLUMN IF EXISTS billing_reason;
DROP TABLE IF EXISTS subscription_item_thresholds;
ALTER TABLE subscriptions
    DROP CONSTRAINT IF EXISTS subscriptions_billing_threshold_amount_gte_check,
    DROP COLUMN IF EXISTS billing_threshold_reset_cycle,
    DROP COLUMN IF EXISTS billing_threshold_amount_gte;
