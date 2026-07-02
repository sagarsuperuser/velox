-- 0126 down: drop the payment-anomaly marker columns.
ALTER TABLE invoices
    DROP COLUMN IF EXISTS payment_anomaly_kind,
    DROP COLUMN IF EXISTS payment_anomaly_payment_intent_id,
    DROP COLUMN IF EXISTS payment_anomaly_captured_cents;
