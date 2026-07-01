-- 0124: RLS on dashboard_sessions + user_tenants — the last two tenant_id
-- tables without it.
--
-- Both were created with a tenant_id column but no RLS at all (no ENABLE, no
-- FORCE, no policy) — the only tables outside the "every tenant table gets
-- ENABLE + FORCE + tenant_isolation" principle (0006, re-affirmed by the
-- 0111/0113 fixes). dashboard_sessions was flagged by audit; user_tenants was
-- found by the new discovery test (TestRLSIsolation_EveryTenantTableIsFenced),
-- which enumerates tenant_id tables from information_schema instead of a
-- hand-maintained list — the reason it caught what the audit missed.
--
-- Neither is exploitable today — both are auth-domain tables reached ONLY via
-- TxBypass, and only by keys that are themselves credentials or identities:
--   * dashboard_sessions by id_hash (sha256 of the raw session token; the
--     lookup key IS the credential) or user_id (internal/session/store.go);
--   * user_tenants by user_id (internal/user/postgres.go — the login flow
--     discovers the tenant FROM the user, so there is no tenant context to
--     scope by at read time).
-- But that safety is behavioral, not structural: a future tenant-scoped query
-- ("this tenant's active sessions"; "this tenant's members" once invites land —
-- the junction's stated purpose) would have zero DB-level isolation, and the
-- silent omission is exactly how the 0111 (ENABLE-without-FORCE on
-- stripe_provider_credentials) and 0113 (feature_flag_overrides) gaps slipped
-- in. The token-authed siblings (payment_update_tokens,
-- customer_portal_magic_links) already carry the backstop; bring these in line.
--
-- Nothing breaks: all existing access runs under TxBypass, which the policies'
-- bypass clause admits. dashboard_sessions carries livemode, so it takes the
-- mode-aware policy shape (0020, same as test_clocks); user_tenants is
-- mode-less auth state and takes the plain 0006 shape.

ALTER TABLE dashboard_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE dashboard_sessions FORCE  ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON dashboard_sessions FOR ALL USING (
    current_setting('app.bypass_rls', true) = 'on'
    OR (
        tenant_id = current_setting('app.tenant_id', true)
        AND livemode = (current_setting('app.livemode', true) IS DISTINCT FROM 'off')
    )
);

ALTER TABLE user_tenants ENABLE ROW LEVEL SECURITY;
ALTER TABLE user_tenants FORCE  ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON user_tenants FOR ALL USING (
    current_setting('app.bypass_rls', true) = 'on'
    OR tenant_id = current_setting('app.tenant_id', true)
);
