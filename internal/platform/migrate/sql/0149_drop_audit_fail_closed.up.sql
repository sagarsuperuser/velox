-- Drop tenant_settings.audit_fail_closed (ADR-089 + ADR-090).
--
-- The column chose, per tenant, whether an audit-write failure should replace
-- an ALREADY-COMMITTED mutation's 2xx with a 503 audit_error. ADR-089 retired
-- that swap: the business tx had committed, so the 503 was a lie — and the
-- Idempotency layer cached the lie for 24h, permanently stranding the real
-- response and inviting a fresh-key double-mutation. The code stopped reading
-- the column then; it was left in place deliberately, to be dropped by the
-- audit redesign's uninstall (this migration) rather than in the interim step.
--
-- Fail-closed semantics did not disappear — they became STRUCTURAL. Audit rows
-- now ride the business transaction (audit.Logger.LogInTx, ADR-090): the
-- mutation and its evidence commit or roll back together, for every tenant,
-- with no post-commit window left to police and no response to swap. A knob
-- that can only be answered "yes, always" is not a setting.
ALTER TABLE tenant_settings
    DROP COLUMN audit_fail_closed;
