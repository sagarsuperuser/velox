-- Reverse migration 0060: re-impose NOT NULL on invoices.subscription_id.
-- Will fail if any one-off invoices exist (subscription_id IS NULL rows);
-- operators must delete or backfill those rows before downgrading.
ALTER TABLE invoices ALTER COLUMN subscription_id SET NOT NULL;
