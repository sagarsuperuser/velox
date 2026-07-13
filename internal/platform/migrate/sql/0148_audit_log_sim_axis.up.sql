-- Sim-time axis columns (ADR-090 arc PR3; amends ADR-086's deferral — the
-- named trigger, "an audit need for sim-time queries", is met: after clock
-- teardown the audit log is the simulation's ONLY surviving record, yet it
-- could not be filtered or ordered by simulated time (metadata-JSON only).
--
-- Nullable by design: only actions on clock-pinned entities carry sim
-- context. The audit writer stamps both columns from the ctx clock binding (and
-- mirrors the legacy metadata keys the dashboard reads); query params / UI
-- filters ship only once stamping reaches parity across writers, so the filter
-- never lies by omission.
--
-- sim_effective_at is the simulated instant the clock STOOD AT when the mutation
-- was performed — not the period the mutation was about. An advance does not
-- replay time; it stands at the new instant and settles everything that came
-- due, so every row one advance produces shares one instant. The axis separates
-- advances, not the periods inside an advance.
ALTER TABLE audit_log ADD COLUMN sim_effective_at TIMESTAMPTZ;
ALTER TABLE audit_log ADD COLUMN test_clock_id TEXT;

-- Partial: the overwhelming majority of rows are real-time (no clock), so the
-- index carries only the simulated slice — and is eligible only when the query
-- proves the column is non-NULL, which the clock-equality predicate does.
--
-- Keyed for the query that actually runs: equality on (tenant_id, livemode,
-- test_clock_id), then the list's sort/seek columns. Audit rows are ordered by
-- created_at DESC, id DESC — there is deliberately no "order by simulated time"
-- (within a clock it is the same order, advances being monotonic; across clocks
-- it interleaves unrelated simulations) — and the cursor seeks on that same
-- (created_at, id) tuple, so this shape serves filter + sort + seek with no sort
-- step. A sim_from/sim_to window is then a cheap filter inside an already-narrow
-- clock slice.
CREATE INDEX idx_audit_log_clock
    ON audit_log (tenant_id, livemode, test_clock_id, created_at DESC, id DESC)
    WHERE test_clock_id IS NOT NULL;
