-- Revert: drop 'subscription_update' from the billing_reason CHECK enum.
-- Destructive if any rows were stamped 'subscription_update' before the
-- down — those rows will fail the new check. Operator must reconcile
-- (UPDATE invoices SET billing_reason = NULL WHERE
-- billing_reason = 'subscription_update') before applying down. The
-- migration runner does not auto-reconcile. (0091 precedent.)

ALTER TABLE invoices
    DROP CONSTRAINT IF EXISTS invoices_billing_reason_check;

ALTER TABLE invoices
    ADD CONSTRAINT invoices_billing_reason_check
        CHECK (billing_reason IS NULL OR billing_reason IN (
            'subscription_cycle', 'subscription_create', 'subscription_cancel',
            'manual', 'threshold'
        ));
