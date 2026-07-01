DROP POLICY IF EXISTS tenant_isolation ON dashboard_sessions;
ALTER TABLE dashboard_sessions NO FORCE ROW LEVEL SECURITY;
ALTER TABLE dashboard_sessions DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_isolation ON user_tenants;
ALTER TABLE user_tenants NO FORCE ROW LEVEL SECURITY;
ALTER TABLE user_tenants DISABLE ROW LEVEL SECURITY;
