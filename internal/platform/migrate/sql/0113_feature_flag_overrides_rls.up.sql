-- feature_flag_overrides was created in 0001 without RLS — the only
-- tenant-scoped table left exempt. A superuser DATABASE_URL bypasses RLS
-- everywhere, but under the velox_app role (the supported deploy posture)
-- this table was readable/writable across tenants. Bring it in line with
-- every other tenant table: tenant-only policy, no livemode predicate
-- (the table has no livemode column — it's account-level config, per 0020).
ALTER TABLE feature_flag_overrides ENABLE ROW LEVEL SECURITY;
ALTER TABLE feature_flag_overrides FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON feature_flag_overrides FOR ALL USING (
    current_setting('app.bypass_rls', true) = 'on'
    OR tenant_id = current_setting('app.tenant_id', true)
);
-- velox_app already holds GRANT ALL on this table from 0001's grant loop;
-- RLS narrows that grant to the caller's tenant, no new grant needed.
