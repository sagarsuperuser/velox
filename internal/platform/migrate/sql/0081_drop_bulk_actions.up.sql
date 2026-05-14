-- Drop bulk_actions table. The internal/bulkaction package and its
-- /v1/admin/bulk_actions API surface were removed during the
-- pre-demo wedge-alignment trim (2026-05-14). No DP demand, no UI page,
-- no MANUAL_TEST coverage — generic operator-cohort feature that doesn't
-- serve the AI-native wedge. See ADR / CHANGELOG for context.
--
-- Re-add cost ~4-6 days if a future DP needs it; trivial CRUD + cohort
-- selection. Don't restore from this migration without first confirming
-- the use case is real.

DROP TABLE IF EXISTS bulk_actions;
