-- ADR-086, Phase 2: give customers a DURABLE "born under a test clock"
-- discriminator so operational planes (analytics now; more later) can ignore
-- simulated rows by an immutable fact instead of the mutable test_clock_id pin.
--
-- sim_clock_id equals test_clock_id AT CREATION but is IMMUTABLE — clock-delete
-- detaches a customer by nulling customers.test_clock_id (ADR-016), which
-- silently un-marked the row as simulated and let the wall-clock planes
-- re-count it. sim_clock_id survives the detach. is_simulated is GENERATED from
-- it (one source of truth, un-nullable, no drift).
ALTER TABLE customers ADD COLUMN sim_clock_id text;
ALTER TABLE customers ADD COLUMN is_simulated boolean
    GENERATED ALWAYS AS (sim_clock_id IS NOT NULL) STORED;

-- Backfill currently-pinned customers (test_clock_id still points at a live
-- clock). Already-detached customers lost their pin — acceptable pre-launch
-- (0 real customers); no dedicated backfill tool (ADR-086 no-backfill posture).
UPDATE customers SET sim_clock_id = test_clock_id WHERE test_clock_id IS NOT NULL;

-- sim ⇒ test-mode, DB-enforced and fail-closed (mirrors the test_clocks and
-- customers.test_clock_id livemode CHECKs added in 0020).
ALTER TABLE customers ADD CONSTRAINT customers_sim_not_live
    CHECK (NOT (is_simulated AND livemode));

-- The analytics count metrics scan a created_at window filtered to
-- is_simulated = false; keep that plan fast as the table grows.
CREATE INDEX idx_customers_live_created ON customers (tenant_id, created_at)
    WHERE is_simulated = false;
