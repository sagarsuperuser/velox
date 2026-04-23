-- Customer-scoped coupon assignment: a `coupon_redemptions` row where
-- both subscription_id and invoice_id are NULL. This is the operator's
-- "apply coupon to this customer's future invoices" primitive — the
-- billing engine re-reads the row on every invoice generation and
-- recomputes the discount against that invoice's actual subtotal, so
-- percentage coupons stay correct as subtotals vary month to month.
--
-- No schema change is required: the columns are already nullable
-- (enforced by 0043 coupons_v2). This migration only adds the partial
-- index that ApplyToInvoiceForCustomer hits on every invoice.
--
-- At-most-one-active-assignment-per-customer is enforced at the service
-- layer (revoke existing before create new) rather than via a UNIQUE
-- partial index, because duration-exhausted rows keep voided_at NULL —
-- matching subscription-scoped semantics where exhausted redemptions
-- remain as historical artifacts but filter out of ApplyToInvoice via
-- durationHasPeriodLeft.
CREATE INDEX idx_coupon_redemptions_active_customer
    ON coupon_redemptions (tenant_id, customer_id)
    WHERE subscription_id IS NULL
      AND invoice_id IS NULL
      AND voided_at IS NULL;
