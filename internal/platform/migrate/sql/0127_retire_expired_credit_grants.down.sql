-- Reverse: nothing to do. The up-migration is a data repair that
-- retires already-expired grants (consumed_cents = amount_cents where
-- an expiry entry exists). The pre-repair consumed_cents values are
-- not recorded, and un-retiring the grants would re-open the
-- backdated-apply negative-ledger hole this migration closes (P14 /
-- ADR-071). Leave the rows retired.
SELECT 1;
