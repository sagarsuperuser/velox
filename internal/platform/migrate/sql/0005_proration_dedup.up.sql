-- Provenance-based dedup for plan-change proration artifacts.
--
-- When a plan change commits but the downstream proration step fails, retries
-- need to be safe: no duplicate invoices, no double-credit grants. We dedup on
-- the natural key of the triggering event: the plan-change timestamp.
--
-- Modeled on Stripe's `invoice.subscription_proration_date` — the server-side
-- idempotency key for plan-change-derived artifacts. Unlike a client-supplied
-- idempotency key (which the client has no way to provide for a
-- server-initiated side-effect), this key is derived from durable state on
-- the subscription and is stable across retries.
--
-- Partial indexes (WHERE NOT NULL) keep the constraint scoped to proration
-- rows only — regular cycle invoices and non-proration credit grants are
-- unaffected. Nullable source columns preserve backward-compatibility: rows
-- written before this migration, and rows from non-proration flows, simply
-- don't populate the columns.

ALTER TABLE invoices ADD COLUMN source_plan_changed_at TIMESTAMPTZ;

CREATE UNIQUE INDEX idx_invoices_proration_dedup
    ON invoices (tenant_id, subscription_id, source_plan_changed_at)
    WHERE source_plan_changed_at IS NOT NULL;

ALTER TABLE customer_credit_ledger
    ADD COLUMN source_subscription_id TEXT,
    ADD COLUMN source_plan_changed_at TIMESTAMPTZ;

CREATE UNIQUE INDEX idx_credit_ledger_proration_dedup
    ON customer_credit_ledger (tenant_id, source_subscription_id, source_plan_changed_at)
    WHERE source_subscription_id IS NOT NULL AND source_plan_changed_at IS NOT NULL;
