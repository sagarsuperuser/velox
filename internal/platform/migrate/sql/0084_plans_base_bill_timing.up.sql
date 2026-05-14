-- Per-plan base bill_timing (ADR-031).
--
-- Velox previously billed exclusively in-arrears: a subscription's
-- base fee landed at the END of the period alongside usage. This
-- column adds the in_advance option for the recurring base — usage
-- stays structurally arrears-only because future-period quantities
-- are unknown.
--
-- Additive migration: default 'in_arrears' preserves every existing
-- tenant's behaviour. The new in_advance code path (first-invoice-
-- on-create + cancel proration) is silent unless an operator opts a
-- plan into in_advance.
--
-- Forward-compatibility note: if a future schema splits the base
-- row into a first-class plan_prices table with per-row bill_timing,
-- the value here becomes the canonical base row's bill_timing. No
-- data migration needed.

ALTER TABLE plans
    ADD COLUMN base_bill_timing TEXT NOT NULL DEFAULT 'in_arrears'
        CHECK (base_bill_timing IN ('in_advance', 'in_arrears'));
