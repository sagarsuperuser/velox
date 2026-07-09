-- Restore the ADR-016 soft-delete column + partial live index (migration 0073).
-- Rows deleted under the teardown model are already physically gone, so there is
-- nothing to un-delete; every surviving clock gets deleted_at = NULL (live),
-- which is the correct state.

ALTER TABLE test_clocks ADD COLUMN deleted_at TIMESTAMPTZ;
CREATE INDEX idx_test_clocks_live ON test_clocks (tenant_id, created_at DESC) WHERE deleted_at IS NULL;
