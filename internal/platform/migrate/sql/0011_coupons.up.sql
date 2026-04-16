-- Coupon / discount system

CREATE TABLE IF NOT EXISTS coupons (
    id              TEXT PRIMARY KEY,
    tenant_id       TEXT NOT NULL REFERENCES tenants(id),
    code            TEXT NOT NULL,
    name            TEXT NOT NULL DEFAULT '',
    type            TEXT NOT NULL DEFAULT 'percentage',
    amount_off      BIGINT NOT NULL DEFAULT 0,
    percent_off     NUMERIC(5,2) NOT NULL DEFAULT 0,
    currency        TEXT NOT NULL DEFAULT '',
    max_redemptions INT,
    times_redeemed  INT NOT NULL DEFAULT 0,
    expires_at      TIMESTAMPTZ,
    active          BOOLEAN NOT NULL DEFAULT true,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(tenant_id, code)
);

CREATE INDEX idx_coupons_tenant_active ON coupons (tenant_id, active);

ALTER TABLE coupons ENABLE ROW LEVEL SECURITY;
ALTER TABLE coupons FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON coupons FOR ALL USING (
    current_setting('app.bypass_rls', true) = 'on'
    OR tenant_id = current_setting('app.tenant_id', true)
);
GRANT ALL ON TABLE coupons TO velox_app;

CREATE TABLE IF NOT EXISTS coupon_redemptions (
    id              TEXT PRIMARY KEY,
    tenant_id       TEXT NOT NULL REFERENCES tenants(id),
    coupon_id       TEXT NOT NULL REFERENCES coupons(id),
    customer_id     TEXT NOT NULL,
    subscription_id TEXT,
    invoice_id      TEXT,
    discount_cents  BIGINT NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_coupon_redemptions_coupon ON coupon_redemptions (tenant_id, coupon_id, created_at);
CREATE INDEX idx_coupon_redemptions_customer ON coupon_redemptions (tenant_id, customer_id);

ALTER TABLE coupon_redemptions ENABLE ROW LEVEL SECURITY;
ALTER TABLE coupon_redemptions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON coupon_redemptions FOR ALL USING (
    current_setting('app.bypass_rls', true) = 'on'
    OR tenant_id = current_setting('app.tenant_id', true)
);
GRANT ALL ON TABLE coupon_redemptions TO velox_app;
