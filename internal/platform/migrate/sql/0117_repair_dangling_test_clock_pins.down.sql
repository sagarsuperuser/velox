-- Irreversible by design: this is a data repair, not a schema change.
-- The original (dangling) test_clock_id values cannot be reconstructed —
-- the clocks they referenced are soft-deleted and re-pinning to them
-- would only recreate the stranding bug. No-op down so a full migrate
-- down/up round-trips cleanly. (No schema object is created here, so
-- there is nothing to drop; see ADR-016/027.)
SELECT 1;
