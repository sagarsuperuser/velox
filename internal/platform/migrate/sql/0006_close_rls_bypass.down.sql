DROP POLICY IF EXISTS tenant_isolation ON payment_update_tokens;
DROP POLICY IF EXISTS tenant_isolation ON idempotency_keys;
DROP POLICY IF EXISTS tenant_isolation ON tenant_settings;

ALTER TABLE payment_update_tokens NO FORCE ROW LEVEL SECURITY;
ALTER TABLE payment_update_tokens DISABLE ROW LEVEL SECURITY;

ALTER TABLE idempotency_keys NO FORCE ROW LEVEL SECURITY;
ALTER TABLE idempotency_keys DISABLE ROW LEVEL SECURITY;

ALTER TABLE tenant_settings NO FORCE ROW LEVEL SECURITY;
ALTER TABLE tenant_settings DISABLE ROW LEVEL SECURITY;
