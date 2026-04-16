-- Rollback: remove Stripe Tax feature flag
DELETE FROM feature_flags WHERE key = 'billing.stripe_tax';
