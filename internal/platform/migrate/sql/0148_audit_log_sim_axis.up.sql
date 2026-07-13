-- Sim-time axis columns (ADR-090 arc PR3; amends ADR-086's deferral — the
-- named trigger, "an audit need for sim-time queries", is met: after clock
-- teardown the audit log is the simulation's ONLY surviving record, yet it
-- could not be filtered or ordered by simulated time (metadata-JSON only).
--
-- Nullable by design: only actions on clock-pinned entities carry sim
-- context. LogInTx stamps both columns (and mirrors the legacy metadata keys
-- the dashboard reads); query params / UI filters ship only once stamping
-- reaches parity across writers (PR7) so the filter never lies by omission.
ALTER TABLE audit_log ADD COLUMN sim_effective_at TIMESTAMPTZ;
ALTER TABLE audit_log ADD COLUMN test_clock_id TEXT;

-- Partial: the overwhelming majority of rows are real-time (no clock); the
-- index only carries the simulated slice, keyed the way forensics asks the
-- question — "what happened on THIS clock, in sim order".
CREATE INDEX idx_audit_log_clock ON audit_log (tenant_id, test_clock_id, sim_effective_at DESC)
    WHERE test_clock_id IS NOT NULL;
