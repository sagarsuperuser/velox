-- Add 'subscription_update' to the invoices.billing_reason CHECK enum.
--
-- The mid-period proration invoice (plan upgrade / quantity increase /
-- item add, cut by the subscription handler) persisted billing_reason
-- NULL — the only invoice writer that stamped no reason — so the
-- dashboard and reporting couldn't say what triggered it. It now stamps
-- 'subscription_update', Stripe's exact billing_reason for the same
-- invoice class.
--
-- Drop-and-recreate is the idiomatic Postgres path for evolving a
-- CHECK enum (no native ALTER for this; 0091 precedent). Safe online:
-- no row scan required because the new constraint is a strict superset
-- of the old.

ALTER TABLE invoices
    DROP CONSTRAINT IF EXISTS invoices_billing_reason_check;

ALTER TABLE invoices
    ADD CONSTRAINT invoices_billing_reason_check
        CHECK (billing_reason IS NULL OR billing_reason IN (
            'subscription_cycle', 'subscription_create', 'subscription_cancel',
            'subscription_update', 'manual', 'threshold'
        ));
