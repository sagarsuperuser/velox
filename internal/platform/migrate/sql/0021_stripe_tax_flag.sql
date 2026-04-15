-- Add Stripe Tax feature flag (disabled by default — opt-in per tenant)
INSERT INTO feature_flags (key, enabled, description) VALUES
    ('billing.stripe_tax', FALSE, 'Use Stripe Tax API for automatic jurisdiction-based tax calculation')
ON CONFLICT DO NOTHING;
