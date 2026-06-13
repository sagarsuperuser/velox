-- Repair dangling test-clock pins left behind by soft-deleted clocks.
--
-- `customers.test_clock_id` and `subscriptions.test_clock_id` carry
-- `ON DELETE SET NULL`, but ADR-016 made test clocks SOFT-deleted
-- (deleted_at, not a row DELETE), so that FK cascade never fires. After
-- a clock was deleted, its pinned customers kept pointing at it (ADR-027
-- moved the pin to the customer), and any subscription created for such a
-- customer inherited the dead clock — landing stranded: excluded from the
-- wall-clock cron (it's pinned) AND from the catchup path (the clock is
-- deleted), so it never bills. The application fix (testclock Delete now
-- detaches customers) prevents new breakage; this one-time repair cleans
-- rows already broken by the shipped behavior.
--
-- Idempotent: re-running matches nothing once the pins are cleared.

-- 1. Detach customers pinned to a soft-deleted clock — realize the FK's
--    intended SET NULL. Their next subscription becomes a clean wall-clock
--    sub instead of inheriting the dead clock.
UPDATE customers c
   SET test_clock_id = NULL, updated_at = now()
 WHERE c.test_clock_id IS NOT NULL
   AND EXISTS (
     SELECT 1 FROM test_clocks tc
      WHERE tc.id = c.test_clock_id
        AND tc.deleted_at IS NOT NULL
   );

-- 2. Un-strand ACTIVE subscriptions pinned to a soft-deleted clock. These
--    were created AFTER the clock was deleted (the bug) — so the resolver
--    fell back to wall-clock at create time and their period fields are
--    already wall-clock; detaching makes them bill normally. Canceled /
--    archived subs (the legitimate cascade-cancel of a deleted clock's
--    subs) KEEP their pointer as the denormalized historical cache.
UPDATE subscriptions s
   SET test_clock_id = NULL, updated_at = now()
 WHERE s.test_clock_id IS NOT NULL
   AND s.status NOT IN ('canceled', 'archived')
   AND EXISTS (
     SELECT 1 FROM test_clocks tc
      WHERE tc.id = s.test_clock_id
        AND tc.deleted_at IS NOT NULL
   );
