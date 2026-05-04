-- Add 'provider_not_configured' to the tax_error_code CHECK
-- constraint.
--
-- The new code shipped in commit d18f9ac ("friendly copy for
-- provider not configured") added ErrCodeProviderNotConfigured
-- to internal/tax/classify.go and wired the classifier to emit
-- it for the "no client configured for livemode=…" error path
-- — but the DB constraint installed by migration 0067 was
-- never updated to allow the new value. Result: when tax calc
-- fails with the new code on a fresh subscription_cycle
-- invoice, the INSERT itself trips SQLSTATE 23514, the
-- engine's RunCycle reports a billing run error, the test-clock
-- catchup propagates it as a failure, and the whole advance
-- ends up in internal_failure.
--
-- Caught the day after the new code shipped because the new
-- ADR-018 failure-reason UI surfaced the SQLSTATE 23514 string
-- on the failed-clock card. Without that UI it would have been
-- a silent slog line.
--
-- Postgres can't ADD VALUE to an inline CHECK; the only path is
-- DROP + ADD with the expanded list. The new constraint is a
-- strict superset — every value previously allowed is still
-- allowed.

ALTER TABLE invoices DROP CONSTRAINT invoices_tax_error_code_check;

ALTER TABLE invoices ADD CONSTRAINT invoices_tax_error_code_check
    CHECK (tax_error_code IS NULL OR tax_error_code IN (
        'customer_data_invalid',
        'jurisdiction_unsupported',
        'provider_outage',
        'provider_auth',
        'provider_not_configured',
        'unknown'
    ));
