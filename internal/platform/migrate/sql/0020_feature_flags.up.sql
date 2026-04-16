CREATE TABLE feature_flags (
    key         TEXT PRIMARY KEY,
    enabled     BOOLEAN NOT NULL DEFAULT FALSE,
    description TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Per-tenant overrides (optional, takes precedence over global)
CREATE TABLE feature_flag_overrides (
    flag_key    TEXT NOT NULL REFERENCES feature_flags(key) ON DELETE CASCADE,
    tenant_id   TEXT NOT NULL,
    enabled     BOOLEAN NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (flag_key, tenant_id)
);

-- Seed default flags
INSERT INTO feature_flags (key, description) VALUES
    ('billing.auto_charge', 'Auto-charge invoices when payment method is on file'),
    ('billing.tax_basis_points', 'Use basis-point integer math for tax calculations'),
    ('webhooks.enabled', 'Enable outbound webhook delivery'),
    ('dunning.enabled', 'Enable dunning retry for failed payments'),
    ('credits.auto_apply', 'Auto-apply credits during billing cycle')
ON CONFLICT DO NOTHING;
