-- Drop test_clocks soft-delete (reverses ADR-016 / migration 0073).
--
-- ADR-086 (Design B) makes a test clock a COMPLETE-teardown unit: deleting a
-- clock now hard-deletes the clock row and tears down its entire simulated
-- customer graph in one transaction (internal/testclock.Delete). The soft-delete
-- model is gone — `deleted_at` is never written anymore, so every read path's
-- `deleted_at IS NULL` filter is vestigial and the column is dead weight that
-- also lies about the schema (it implies clocks are soft-deletable; they are
-- not). The clock's history is preserved by the audit_log DELETE entry, not by a
-- tombstone row.
--
-- Drop the column and its partial hot-path index. The plain
-- idx_test_clocks_tenant (tenant_id, created_at DESC) from migration 0020 still
-- backs the List query, so no replacement index is needed.

DROP INDEX IF EXISTS idx_test_clocks_live;
ALTER TABLE test_clocks DROP COLUMN deleted_at;
