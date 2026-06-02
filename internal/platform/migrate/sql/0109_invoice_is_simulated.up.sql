-- ADR-030: persist, at write time, whether an invoice's domain timestamps were
-- stamped on a test clock's simulated time. The activity timeline + invoice
-- header read this authoritative flag instead of re-deriving simulation status
-- from the subscription/customer test_clock_id at read time — a read-time
-- derivation is a mutable snapshot (unpinning a clock would retroactively drop
-- the badge) and misses manual one-off invoices, which have no subscription to
-- look through. Wall-clock invoices (the overwhelming majority, and every
-- live-mode invoice) default false.
ALTER TABLE invoices ADD COLUMN is_simulated BOOLEAN NOT NULL DEFAULT false;
