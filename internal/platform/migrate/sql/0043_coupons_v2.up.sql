-- Coupons v2: atomic redemption, lifecycle, and restrictions bag.
--
-- Five changes, all aimed at landing the coupon model on industry-standard
-- footing before launch:
--
--   1. Source of truth for percentage discounts becomes percent_off_bp
--      (basis points, INT). The floating-point percent_off column was a
--      source of rounding drift and two-field-in-sync confusion — backfill
--      into _bp, drop the float.
--
--   2. active BOOLEAN -> archived_at TIMESTAMPTZ. "Active" was a derived
--      state masquerading as a column: a coupon is live if it isn't
--      archived, hasn't expired, and hasn't hit max_redemptions. Flipping
--      one field (active=false) when three gates disagree was the wrong
--      shape; archived_at is the single user-controlled lifecycle knob.
--
--   3. restrictions JSONB holds the long-tail of rarely-used eligibility
--      gates (min_amount_cents, first_time_customer_only,
--      max_redemptions_per_customer). Top-level columns stay for the hot
--      path (plan_ids, customer_id, stackable, duration). The bag lets
--      future restrictions land without another migration.
--
--   4. metadata JSONB — unopinionated tenant-side key/value store,
--      standard across Stripe/Chargebee/Recurly coupon APIs.
--
--   5. Correctness: UNIQUE (tenant_id, coupon_id, subscription_id) on
--      coupon_redemptions prevents the race where two concurrent redeems
--      attach the same coupon to the same subscription twice. Partial
--      UNIQUE on (tenant_id, idempotency_key) supports safe retry on the
--      redeem endpoint — same key, same response, no double-discount.
--
-- No data loss: percent_off is backfilled into percent_off_bp before drop,
-- and the down migration reverses the transform. active=false rows become
-- archived_at=now() so the semantics survive the rename.

-- (1) percent_off -> percent_off_bp as source of truth.
UPDATE coupons
SET percent_off_bp = ROUND(percent_off * 100)::INT
WHERE percent_off_bp = 0 AND percent_off > 0;

ALTER TABLE coupons DROP COLUMN percent_off;

-- (2) active -> archived_at.
ALTER TABLE coupons ADD COLUMN archived_at TIMESTAMPTZ;
UPDATE coupons SET archived_at = updated_at WHERE active = false;
DROP INDEX IF EXISTS idx_coupons_tenant_active;
ALTER TABLE coupons DROP COLUMN active;
CREATE INDEX idx_coupons_tenant_archived
    ON coupons (tenant_id, archived_at)
    WHERE archived_at IS NULL;

-- (3) + (4) restrictions + metadata bags.
ALTER TABLE coupons
    ADD COLUMN restrictions JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN metadata     JSONB NOT NULL DEFAULT '{}'::jsonb;

-- (5) Redemption uniqueness + idempotency.
ALTER TABLE coupon_redemptions ADD COLUMN idempotency_key TEXT;

-- Same (tenant, coupon, subscription) can only produce one redemption row.
-- Prevents the double-apply race where two requests for the same code on
-- the same sub land between the "check count" and "insert redemption"
-- steps of the old flow. NULL subscription_id means a one-off invoice
-- redeem — those use the invoice_id uniqueness guard instead.
CREATE UNIQUE INDEX idx_coupon_redemptions_subscription_unique
    ON coupon_redemptions (tenant_id, coupon_id, subscription_id)
    WHERE subscription_id IS NOT NULL;

-- Invoice-scoped redemptions (no subscription) still need their own
-- dedupe key.
CREATE UNIQUE INDEX idx_coupon_redemptions_invoice_unique
    ON coupon_redemptions (tenant_id, coupon_id, invoice_id)
    WHERE invoice_id IS NOT NULL AND subscription_id IS NULL;

-- Idempotency: same key -> same redemption row. Partial so redemptions
-- without a key (the legacy / internal-caller path) don't collide.
CREATE UNIQUE INDEX idx_coupon_redemptions_idempotency
    ON coupon_redemptions (tenant_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;
