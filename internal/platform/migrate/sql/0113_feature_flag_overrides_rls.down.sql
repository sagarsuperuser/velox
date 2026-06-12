DROP POLICY IF EXISTS tenant_isolation ON feature_flag_overrides;
ALTER TABLE feature_flag_overrides NO FORCE ROW LEVEL SECURITY;
ALTER TABLE feature_flag_overrides DISABLE ROW LEVEL SECURITY;
