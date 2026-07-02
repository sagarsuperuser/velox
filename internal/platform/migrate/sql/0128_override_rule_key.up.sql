-- P4 (ADR-070): key customer price overrides by rule_key, not
-- rating_rule_version_id. Overrides were stored and fetched by version
-- id while the single-rule cycle close resolved the LATEST version by
-- key — publishing v2 of a rule silently detached every v1-keyed
-- override and the customer reverted to list price (audit High #3).
-- A negotiated price follows the RULE across version publishes.

ALTER TABLE customer_price_overrides ADD COLUMN rule_key TEXT;

-- Overrides become append-only effectivity rows (ADR-070: every rate-
-- state writer is effective-next-period; the close resolves the row in
-- force at period OPEN, which requires history — a mutated single row
-- can't answer "what was the negotiated price when this period
-- opened"). An upsert now deactivates the prior row (stamping
-- deactivated_at) and inserts a fresh one; [created_at, deactivated_at)
-- is the row's effectivity window.
ALTER TABLE customer_price_overrides ADD COLUMN deactivated_at TIMESTAMPTZ;

-- Rows deactivated before this migration: best-effort window close at
-- their last update.
UPDATE customer_price_overrides
SET deactivated_at = updated_at
WHERE active = false;

UPDATE customer_price_overrides o
SET rule_key = v.rule_key
FROM rating_rule_versions v
WHERE v.id = o.rating_rule_version_id;

-- Collision collapse: the old unique was per VERSION, so a customer may
-- legitimately hold active overrides against v1 AND v2 of one rule_key
-- (the natural workaround for the detach bug was re-creating the
-- override after each publish). Deterministic winner: the row bound to
-- the highest rule version, tie-break latest updated_at, then id.
-- Losers flip active=false and are KEPT — they are the audit trail of
-- the demotion.
WITH ranked AS (
    SELECT o.id,
           ROW_NUMBER() OVER (
               PARTITION BY o.tenant_id, o.customer_id, o.rule_key
               ORDER BY v.version DESC, o.updated_at DESC, o.id DESC
           ) AS rn
    FROM customer_price_overrides o
    JOIN rating_rule_versions v ON v.id = o.rating_rule_version_id
    WHERE o.active
)
UPDATE customer_price_overrides o
SET active = false, deactivated_at = now(), updated_at = now()
FROM ranked r
WHERE o.id = r.id AND r.rn > 1;

ALTER TABLE customer_price_overrides ALTER COLUMN rule_key SET NOT NULL;

-- One ACTIVE override per (tenant, customer, rule_key). Partial so
-- demoted/deactivated rows persist for audit. CreateOverride's upsert
-- infers this index via ON CONFLICT ... WHERE active.
CREATE UNIQUE INDEX idx_price_overrides_active_rule_key
    ON customer_price_overrides (tenant_id, customer_id, rule_key)
    WHERE active;

-- The per-version unique must go: under rule_key keying a re-created
-- override against a newer version of the same key is the SAME logical
-- override, and this constraint would 409 it.
ALTER TABLE customer_price_overrides
    DROP CONSTRAINT customer_price_overrides_tenant_id_customer_id_rating_rule__key;
