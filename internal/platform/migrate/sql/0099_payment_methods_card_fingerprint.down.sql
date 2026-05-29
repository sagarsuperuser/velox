DROP INDEX IF EXISTS idx_payment_methods_active_fingerprint;
ALTER TABLE payment_methods DROP COLUMN IF EXISTS card_fingerprint;
