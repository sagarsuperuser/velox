-- Stamp WHEN an invoice was marked uncollectible. voided/paid transitions
-- record voided_at/paid_at, but the uncollectible transition recorded no
-- timestamp — so the payment timeline could never show the bad-debt
-- write-off as a dated lifecycle row (the operator-driven
-- POST /{id}/mark-uncollectible path left no chronology at all; the
-- dunning-automated path only showed its dunning twin).
ALTER TABLE invoices ADD COLUMN uncollectible_at TIMESTAMPTZ;
