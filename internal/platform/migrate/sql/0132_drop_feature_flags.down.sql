-- Recreate the dropped tables exactly as 0001 defined them, with 0113's
-- RLS posture and 0002's seed rows (drop-table downs must restore a
-- bootable schema, not a bare shell). Flag VALUES are not recoverable —
-- they re-seed at the 0002 defaults (all FALSE), which matches the
-- subsystem's inert reality.
CREATE TABLE feature_flags (
    key         TEXT PRIMARY KEY,
    enabled     BOOLEAN NOT NULL DEFAULT FALSE,
    description TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE feature_flag_overrides (
    flag_key    TEXT NOT NULL REFERENCES feature_flags(key) ON DELETE CASCADE,
    tenant_id   TEXT NOT NULL,
    enabled     BOOLEAN NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (flag_key, tenant_id)
);

GRANT ALL ON TABLE feature_flags TO velox_app;
GRANT ALL ON TABLE feature_flag_overrides TO velox_app;

-- 0113 RLS posture (tenant-only; the table has no livemode column).
ALTER TABLE feature_flag_overrides ENABLE ROW LEVEL SECURITY;
ALTER TABLE feature_flag_overrides FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON feature_flag_overrides FOR ALL USING (
    current_setting('app.bypass_rls', true) = 'on'
    OR tenant_id = current_setting('app.tenant_id', true)
);

-- 0002 seed rows.
INSERT INTO feature_flags (key, enabled, description) VALUES
    ('billing.auto_charge', FALSE, 'Auto-charge invoices when payment method is on file'),
    ('billing.tax_basis_points', FALSE, 'Use basis-point integer math for tax calculations'),
    ('webhooks.enabled', FALSE, 'Enable outbound webhook delivery'),
    ('dunning.enabled', FALSE, 'Enable dunning retry for failed payments'),
    ('credits.auto_apply', FALSE, 'Auto-apply credits during billing cycle'),
    ('billing.stripe_tax', FALSE, 'Use Stripe Tax API for automatic tax calculation')
ON CONFLICT DO NOTHING;
