-- Close RLS bypass on tenant-scoped tables that were missed in the initial
-- schema. Without RLS, a compromised or misconfigured connection could read
-- or write rows across tenant boundaries even though every row carries a
-- tenant_id column.
--
-- Three tables affected:
--   * tenant_settings     — invoice prefixes, tax rates, company details
--   * idempotency_keys    — cached API responses (may contain PII)
--   * payment_update_tokens — token hashes mapping to customer/invoice
--
-- Policy matches the tenant_isolation pattern established in 0001_schema:
-- access allowed when app.bypass_rls = 'on' (background jobs) OR when
-- app.tenant_id matches the row's tenant_id. Callers refactored in the
-- same commit to use db.BeginTx(ctx, postgres.TxTenant, tenantID).
--
-- Validate() on payment_update_tokens legitimately runs without a tenant
-- context (the token itself is the authentication; tenantID is only known
-- AFTER lookup), and Cleanup() is a cross-tenant background job — both
-- switch to TxBypass with a comment justifying it.

ALTER TABLE tenant_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_settings FORCE ROW LEVEL SECURITY;

ALTER TABLE idempotency_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE idempotency_keys FORCE ROW LEVEL SECURITY;

ALTER TABLE payment_update_tokens ENABLE ROW LEVEL SECURITY;
ALTER TABLE payment_update_tokens FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON tenant_settings FOR ALL USING (
    current_setting('app.bypass_rls', true) = 'on'
    OR tenant_id = current_setting('app.tenant_id', true)
);

CREATE POLICY tenant_isolation ON idempotency_keys FOR ALL USING (
    current_setting('app.bypass_rls', true) = 'on'
    OR tenant_id = current_setting('app.tenant_id', true)
);

CREATE POLICY tenant_isolation ON payment_update_tokens FOR ALL USING (
    current_setting('app.bypass_rls', true) = 'on'
    OR tenant_id = current_setting('app.tenant_id', true)
);
