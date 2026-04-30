-- Forward-only cleanup. Recreating the dropped legacy auth tables
-- would require restoring the original schemas from 0001 / 0034 /
-- 0035 (column types, indexes, FKs, RLS policies) and would not
-- restore the row data — so a "rollback" of 0068 would yield empty
-- shells that no Go package reads. Not worth the maintenance cost on
-- a pre-launch / local-only schema.
--
-- If you genuinely need the tables back, restore them from the
-- corresponding up.sql files manually rather than relying on this
-- migration's down step.

SELECT 1;
