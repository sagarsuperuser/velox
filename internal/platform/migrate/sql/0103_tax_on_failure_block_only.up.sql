-- Migration 0103: tax_on_failure becomes block-only (ADR-041).
--
-- Migration 0039 introduced tax_on_failure with two values, 'block' (default
-- for new tenants) and 'fallback_manual' (set on all existing tenants at
-- migration time to preserve behavior). Per ADR-041 (2026-05-30), the
-- fallback_manual branch is removed — it silently substituted zero tax when
-- no manual rate matched the customer's jurisdiction, overriding operator
-- intent. The engine now always defers to tax_status=pending on provider
-- failure, and the TaxRetrier reconciler picks it up.
--
-- This migration:
--   1. Updates any tenant still at 'fallback_manual' to 'block'.
--   2. Tightens the CHECK constraint to allow only 'block'.
--
-- The column itself is retained for forward compat (future failure-handling
-- shapes can use the same field, e.g. defer_with_delay).

UPDATE tenant_settings
SET tax_on_failure = 'block'
WHERE tax_on_failure = 'fallback_manual';

ALTER TABLE tenant_settings
    DROP CONSTRAINT IF EXISTS tenant_settings_tax_on_failure_check;

ALTER TABLE tenant_settings
    ADD CONSTRAINT tenant_settings_tax_on_failure_check
        CHECK (tax_on_failure = 'block');
