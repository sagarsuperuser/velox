-- P14 (ADR-071): retro-retire credit grants that were expired by the
-- pre-fix sweep. The old expiry path appended the -remaining expiry
-- entry but never touched the grant's consumed_cents, so every grant
-- expired before this migration still matches drainPositiveBlocks'
-- eligibility (consumed_cents < amount_cents) — a backdated
-- ApplyToInvoiceAt (at < expires_at) could re-drain it and push the
-- ledger negative. The new ExpireGrantAtomic flips consumed_cents =
-- amount_cents atomically with the entry; this backfill applies the
-- same retirement to legacy rows so consumed_cents becomes the single
-- structural exclusion and the code can drop the description-LIKE
-- dedup filter (which, without this backfill, was the only thing
-- keeping the sweep from double-expiring legacy grants).
--
-- The exact-match description join is the legacy linkage format
-- (processExpiry wrote 'Expired grant <grant_id>' verbatim), used here
-- one last time. This also repairs grants already corrupted by the
-- backdated-apply bug: any re-opened headroom on an expired grant is
-- closed rather than left spendable.
UPDATE customer_credit_ledger g
SET consumed_cents = g.amount_cents
WHERE g.entry_type = 'grant'
  AND g.consumed_cents < g.amount_cents
  AND EXISTS (
    SELECT 1 FROM customer_credit_ledger e
    WHERE e.tenant_id = g.tenant_id
      AND e.customer_id = g.customer_id
      AND e.entry_type = 'expiry'
      AND e.description = 'Expired grant ' || g.id
  );
