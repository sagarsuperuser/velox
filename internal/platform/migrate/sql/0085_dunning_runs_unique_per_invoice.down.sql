-- Reverse the unique-per-invoice rule. Cannot un-delete the duplicate
-- runs that the up-migration removed (events + runs already gone).
DROP INDEX IF EXISTS idx_dunning_runs_one_per_invoice;
