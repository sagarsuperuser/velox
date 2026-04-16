-- Rollback: drop feature flag tables (cascades overrides)
DROP TABLE IF EXISTS feature_flag_overrides;
DROP TABLE IF EXISTS feature_flags;
