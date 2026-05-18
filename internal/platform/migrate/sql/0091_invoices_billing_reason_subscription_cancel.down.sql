-- Revert: drop 'subscription_cancel' from the billing_reason CHECK enum.
-- Destructive if any rows were stamped 'subscription_cancel' before the
-- down — those rows will fail the new check. Operator must reconcile
-- (UPDATE ... SET billing_reason='subscription_cycle' WHERE
-- billing_reason='subscription_cancel') before applying down. The
-- migration runner does not auto-reconcile.

ALTER TABLE invoices
    DROP CONSTRAINT IF EXISTS invoices_billing_reason_check;

ALTER TABLE invoices
    ADD CONSTRAINT invoices_billing_reason_check
        CHECK (billing_reason IS NULL OR billing_reason IN (
            'subscription_cycle', 'subscription_create', 'manual', 'threshold'
        ));
