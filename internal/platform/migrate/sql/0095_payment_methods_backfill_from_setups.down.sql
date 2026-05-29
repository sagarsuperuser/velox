-- Reverse: nothing to do. The backfill is read-from-summary →
-- write-to-canonical; we don't want to clobber payment_methods rows
-- on a down-migration since they may now be the source of truth for
-- subsequent operations (set-default, remove, etc.). A down-migration
-- that DELETEs rows could lose real data attached AFTER the backfill.
-- Leave the rows in place. Operators rolling back this migration
-- should be aware the backfilled rows persist.
SELECT 1;
