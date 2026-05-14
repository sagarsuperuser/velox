-- Reverse the customer-level test-clock attach.
--
-- This rollback is data-lossy in one specific case: customers that
-- were created in the new flow with a test_clock_id that ISN'T
-- already replicated onto a sub will lose the attach. The
-- application layer always sets sub.test_clock_id from the customer
-- at sub-create, so any customer-with-sub pair is preserved on the
-- sub column. Customers without subs (rare — they wouldn't be
-- billing) lose the link.
--
-- We accept this trade-off because the alternative is keeping a
-- column post-rollback that the application no longer reads, which
-- would silently drift and re-introduce the original ambiguity
-- when the migration is re-applied.

DROP INDEX IF EXISTS idx_customers_test_clock;
ALTER TABLE customers DROP COLUMN IF EXISTS test_clock_id;
