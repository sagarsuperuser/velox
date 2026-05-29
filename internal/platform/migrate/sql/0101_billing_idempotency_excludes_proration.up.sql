-- Exempt proration invoices from the cycle-billing dedup index.
--
-- Background: `idx_invoices_billing_idempotency` was designed to prevent
-- duplicate CYCLE invoices for the same subscription period — the
-- engine's per-period-close idempotency story. But its partial predicate
-- (`WHERE status <> 'voided'`) doesn't exclude proration invoices, so
-- when two distinct mid-cycle item-add prorations land on the same sub
-- in the same wall-clock instant (very common under test-clock-driven
-- flows where every operation happens at frozen_time), they share
-- `(tenant, sub, billing_period_start, billing_period_end)` and the
-- second insert fails on this index — not on `idx_invoices_proration_dedup`
-- (which would have correctly let it through because the item IDs
-- differ).
--
-- The conflation surfaced during 2026-05-28 EX3 manual test: a customer
-- with two items added on the same simulated day produced a proration
-- invoice for item A; the second item's proration tried to insert, hit
-- this index, and the handler's retry-via-GetByProrationSource then
-- queried for item B's row (which never existed) → "proration dedup
-- lookup: not found" error. Item committed but proration line missing.
--
-- Fix: proration invoices have their own dedicated dedup index
-- (idx_invoices_proration_dedup) keyed on the proration source tuple.
-- The billing-idempotency index should only apply to cycle invoices
-- where `source_plan_changed_at IS NULL`. Add that predicate.

DROP INDEX IF EXISTS idx_invoices_billing_idempotency;
CREATE UNIQUE INDEX idx_invoices_billing_idempotency
    ON invoices (tenant_id, subscription_id, billing_period_start, billing_period_end)
    WHERE status <> 'voided' AND source_plan_changed_at IS NULL;
