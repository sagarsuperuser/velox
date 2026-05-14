-- Drop billing_alerts + billing_alert_triggers. The internal/billingalert
-- package and its /v1/billing/alerts API surface were removed during the
-- Phase 2 wedge-alignment trim (2026-05-14). Reasons:
--
--   - No UI page after lean-cut (CHANGELOG carried "backend-only" flag)
--   - No design partner has asked for it; zero observed customer pull
--   - 1700+ LOC of backend with no consumer is maintenance load
--   - The "AI anomaly alerts" pivot is real but speculative until a
--     real DP describes the shape — building now would lock in a guess
--
-- Re-add cost ~2-3 days if a future DP needs anomaly alerts (likely with
-- a different shape — token-spike detection rather than fixed thresholds).
-- Don't restore from this migration without confirming the use case.

DROP TABLE IF EXISTS billing_alert_triggers;
DROP TABLE IF EXISTS billing_alerts;
