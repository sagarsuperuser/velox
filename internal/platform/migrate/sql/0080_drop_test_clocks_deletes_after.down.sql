-- Restore test_clocks.deletes_after and the partial index that
-- supported the TTL sweeper. Both shapes match migration 0020's
-- original definition. The column is restored empty — every row
-- was NULL in the forward direction so there is nothing to back-fill.
ALTER TABLE test_clocks ADD COLUMN deletes_after TIMESTAMPTZ;
CREATE INDEX idx_test_clocks_deletes ON test_clocks (deletes_after) WHERE deletes_after IS NOT NULL;
