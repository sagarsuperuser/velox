-- Expand audit_log.actor_type to admit 'customer' alongside the
-- existing api_key / user / system values. Customer-portal-driven
-- mutations (subscription cancel, payment-method detach, profile
-- edit, etc.) now write an audit_log row tagged with the customer's
-- ID as actor_id and 'customer' as actor_type, so the operator
-- Activity feed and AuditLog page can render customer-initiated
-- events with the right by-line ("by customer") instead of the
-- misleading 'system' fallback.

ALTER TABLE audit_log DROP CONSTRAINT IF EXISTS audit_log_actor_type_check;
ALTER TABLE audit_log
    ADD CONSTRAINT audit_log_actor_type_check
    CHECK (actor_type IN ('api_key', 'user', 'system', 'customer'));
