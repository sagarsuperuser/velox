-- Reversing the soft-delete column drops any rows that were soft-
-- deleted under the new model — without the column, the application
-- can't tell live from deleted, and surfacing previously-hidden
-- rows back to the operator would be a worse outcome than removing
-- them. The hard-delete is acceptable here because down-migrations
-- are operator-initiated rollbacks; the operator either accepts the
-- data loss or reverts the down.

DELETE FROM test_clocks WHERE deleted_at IS NOT NULL;
DROP INDEX IF EXISTS idx_test_clocks_live;
ALTER TABLE test_clocks DROP COLUMN deleted_at;
