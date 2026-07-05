-- ADR-080: paid-commit credit-note relief (ADR-078 phase 2).
--
-- cn_retired_cents: cumulative credits retired from a commit grant by
-- RELIEF credit notes (never by void-retire). This is K in the telescoping
-- refund anchor f(k) = round_half_even(gross_paid * k / granted): each
-- relief refunds f(K+r) - f(K), so any sequence of partial reliefs sums to
-- exactly f(total retired) and can never over-refund the cash collected.
-- Kept on the grant row so ONE row lock freezes every formula input.
ALTER TABLE customer_credit_ledger
    ADD COLUMN cn_retired_cents BIGINT NOT NULL DEFAULT 0;

-- commit_retired_cents: how many commit credits THIS credit note retired
-- (r). Real column, not metadata (D9 discipline): auditors recompute every
-- relief CN's cash from (gross_paid, granted, prior K) and reconcile
-- grant.cn_retired_cents == SUM(cn.commit_retired_cents).
ALTER TABLE credit_notes
    ADD COLUMN commit_retired_cents BIGINT NOT NULL DEFAULT 0;
