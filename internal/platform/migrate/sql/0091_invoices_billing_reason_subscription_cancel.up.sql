-- Add 'subscription_cancel' to the invoices.billing_reason CHECK enum.
--
-- New invoice variant emitted by engine.BillFinalOnImmediateCancel
-- (PR-10): when an operator cancels a sub mid-period, the engine now
-- emits a final invoice covering [current_period_start, canceled_at]
-- with prorated in_arrears base + usage for the elapsed days. Pre-PR-10,
-- mid-period immediate cancels generated NO final invoice, which left
-- partial-period usage unbilled (revenue leak — customer could rack up
-- usage and cancel for free).
--
-- The 'subscription_cancel' billing_reason distinguishes this final
-- invoice from a normal subscription_cycle close invoice so reporting
-- can answer "how much revenue came from cancel-time true-ups" and so
-- the dashboard's invoice list surfaces the right operator-facing label.
--
-- Drop-and-recreate is the idiomatic Postgres path for evolving a
-- CHECK enum (no native ALTER for this). Safe online: no row scan
-- required because the new constraint is a strict superset of the old.

ALTER TABLE invoices
    DROP CONSTRAINT IF EXISTS invoices_billing_reason_check;

ALTER TABLE invoices
    ADD CONSTRAINT invoices_billing_reason_check
        CHECK (billing_reason IS NULL OR billing_reason IN (
            'subscription_cycle', 'subscription_create', 'subscription_cancel', 'manual', 'threshold'
        ));
