-- Soft-delete for test_clocks (ADR-016).
--
-- Velox's existing convention is to soft-delete every entity:
-- tenants/customers/plans/subs use a `status` column with terminal
-- states; coupons + api_keys use timestamp columns (archived_at,
-- revoked_at). Test clocks were the lone hard-delete via
-- `DELETE FROM test_clocks` plus an ON DELETE SET NULL on
-- subscriptions.test_clock_id — which produced silent orphan subs
-- (test_clock_id NULL, billing fields still in simulation time)
-- that the wall-clock scheduler couldn't reconcile, and broke
-- audit_log refs to the just-deleted clock.
--
-- This migration aligns test_clocks with the rest of the schema by
-- adding a deleted_at TIMESTAMPTZ column. The service-layer Delete
-- now updates this column AND cascade-cancels pinned subs in the
-- same tx. Read paths filter WHERE deleted_at IS NULL.
--
-- Why timestamp + not status: every existing test_clocks status
-- value (`ready`, `advancing`, `internal_failure`) describes a
-- live operational state, not a soft-delete marker. Adding a
-- `deleted` status would force an awkward "clocks in advancing
-- state when deleted" question; a separate timestamp column
-- captures lifecycle independently of operational state, mirrors
-- coupons.archived_at + api_keys.revoked_at, and lets us tell
-- "deleted while advancing" apart from "deleted while ready" if
-- forensics ever need it.
--
-- ON DELETE SET NULL is preserved for backward compatibility — if
-- a future operator-driven hard delete is ever needed (out-of-band
-- DBA cleanup), the FK side still degrades gracefully. The service
-- never takes that path.

ALTER TABLE test_clocks ADD COLUMN deleted_at TIMESTAMPTZ;

-- Partial index on the live (non-deleted) set. Most queries scan
-- WHERE deleted_at IS NULL ORDER BY created_at DESC; the partial
-- index keeps it cheap even after the table accumulates soft-
-- deleted rows from the deletes_after sweeper.
CREATE INDEX idx_test_clocks_live ON test_clocks (tenant_id, created_at DESC) WHERE deleted_at IS NULL;
