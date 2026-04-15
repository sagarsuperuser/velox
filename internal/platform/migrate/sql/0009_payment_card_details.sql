-- Store card details from Stripe for display (brand, last4, expiry)
ALTER TABLE customer_payment_setups ADD COLUMN IF NOT EXISTS card_brand TEXT;
ALTER TABLE customer_payment_setups ADD COLUMN IF NOT EXISTS card_last4 TEXT;
ALTER TABLE customer_payment_setups ADD COLUMN IF NOT EXISTS card_exp_month INTEGER;
ALTER TABLE customer_payment_setups ADD COLUMN IF NOT EXISTS card_exp_year INTEGER;
