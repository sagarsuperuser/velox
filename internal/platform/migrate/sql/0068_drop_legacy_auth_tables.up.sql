-- Forward-only cleanup of orphaned tables left behind by the
-- 2026-04-29 auth-revert (ADR-007) and the cut Members feature.
-- The Go packages that read these tables (internal/dashauth,
-- internal/user, internal/dashmembers) were deleted at revert time;
-- the tables stayed because dropping them is destructive and there
-- was no immediate cost. With the schema audited 2026-04-30, the
-- tables are now confirmed unused and worth dropping so future
-- contributors don't have to re-derive that they're dead.
--
-- Tables dropped:
--   users                   — created by 0001_schema; the embedded
--                             auth user store. Pre-dates the
--                             session/password split in 0034.
--   user_tenants            — 0034. The user↔tenant membership join.
--   sessions                — 0034. The pre-revert dashboard session
--                             store. Replaced by dashboard_sessions
--                             (migration 0066, ADR-008) which is the
--                             API-key-derived httpOnly cookie store
--                             used today.
--   password_reset_tokens   — 0034. Password reset flow, deleted at
--                             revert.
--   member_invitations      — 0035. Member invite tokens, deleted at
--                             revert with the dashmembers package.
--
-- DO NOT touch these — they remain active:
--   dashboard_sessions      — 0066, current auth surface
--   customer_portal_sessions — 0022, customer portal auth
--
-- CASCADE drops any indexes / FKs that referenced these tables. RLS
-- policies on the same tables are dropped automatically with the
-- table.

DROP TABLE IF EXISTS member_invitations CASCADE;
DROP TABLE IF EXISTS password_reset_tokens CASCADE;
DROP TABLE IF EXISTS sessions CASCADE;
DROP TABLE IF EXISTS user_tenants CASCADE;
DROP TABLE IF EXISTS users CASCADE;
