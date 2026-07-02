-- 0126: durable payment-anomaly marker (ADR-068). The settle path DETECTS
-- money anomalies (a second different PI on a paid invoice; captured amount
-- != booked amount; payment on a voided invoice) — this marker is what the
-- dashboard attention banner reads. With auto-refund deferred, the operator
-- IS the refund mechanism, so a log line + webhook alone are not an
-- operator surface (audit HIGH #5's core complaint).
--
-- Single-slot by design (last anomaly wins): the banner needs "something is
-- wrong with money on THIS invoice + which PI", not a ledger. The audit log
-- carries the full history.
--
-- NOTE on numbering: the plan reserved 0125+ for P2b(1)/P9(3)/P11(1); P2b
-- uses 0125 AND 0126 — P9/P11 shift to 0127+.
ALTER TABLE invoices
    ADD COLUMN payment_anomaly_kind TEXT,
    ADD COLUMN payment_anomaly_payment_intent_id TEXT,
    ADD COLUMN payment_anomaly_captured_cents BIGINT;
