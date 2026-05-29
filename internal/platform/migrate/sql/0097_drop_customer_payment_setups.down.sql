-- Reverse: nothing to do. customer_payment_setups was a denorm cache
-- that the canonical sources (customers.stripe_customer_id +
-- payment_methods) fully replaced. Recreating the empty schema on
-- rollback wouldn't restore any data — by migration 0097 every
-- reader had already been routed through compositePaymentSetupStore
-- which reads from the canonical sources. If a hard rollback is
-- needed, recover the data from a pre-0097 backup and re-derive the
-- summary rows from customers + payment_methods.
SELECT 1;
