-- Drop plan_migrations table. The internal/planmigration package and
-- its /v1/admin/plan_migrations API surface were removed during the
-- pre-demo wedge-alignment trim (2026-05-14). No DP demand, no UI page,
-- no MANUAL_TEST coverage — generic operator-cohort feature that doesn't
-- serve the AI-native wedge. See CHANGELOG for context.
--
-- Re-add cost ~5-7 days if a future DP needs it (CRUD + before/after
-- preview pair). Don't restore from this migration without first
-- confirming the use case is real.

DROP TABLE IF EXISTS plan_migrations;
