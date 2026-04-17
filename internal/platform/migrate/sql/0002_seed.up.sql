INSERT INTO feature_flags (key, enabled, description) VALUES
    ('billing.auto_charge', FALSE, 'Auto-charge invoices when payment method is on file'),
    ('billing.tax_basis_points', FALSE, 'Use basis-point integer math for tax calculations'),
    ('webhooks.enabled', FALSE, 'Enable outbound webhook delivery'),
    ('dunning.enabled', FALSE, 'Enable dunning retry for failed payments'),
    ('credits.auto_apply', FALSE, 'Auto-apply credits during billing cycle'),
    ('billing.stripe_tax', FALSE, 'Use Stripe Tax API for automatic tax calculation')
ON CONFLICT DO NOTHING;
