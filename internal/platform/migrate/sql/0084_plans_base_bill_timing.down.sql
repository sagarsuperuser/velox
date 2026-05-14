-- Reverse ADR-031: drop base_bill_timing column.
--
-- Rolling back is data-safe — any in_advance plan that produced an
-- already-billed first-period invoice will simply look like a normal
-- arrears invoice once the column is gone. Ledger entries (credit
-- notes from cancel proration) stay intact; they're independent of
-- the plan's current bill_timing value.

ALTER TABLE plans DROP COLUMN IF EXISTS base_bill_timing;
