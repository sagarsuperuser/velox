-- Reversing the backfill would require knowing which rows were
-- originally empty; we don't track that. The down-migration is a
-- no-op — restoring NULL/empty names is destructive and isn't a
-- behaviour the operator can possibly need.
SELECT 1;
