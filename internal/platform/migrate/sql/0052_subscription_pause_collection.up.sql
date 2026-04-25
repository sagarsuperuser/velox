-- Stripe-parity pause_collection on subscriptions. Distinct from the
-- existing hard Pause (status=paused, sub excluded from GetDueBilling) —
-- pause_collection keeps the cycle running but neuters the financial
-- side: invoices generate as draft and skip finalize/charge/dunning.
-- v1 supports only behavior='keep_as_draft'; mark_uncollectible/void
-- need an 'uncollectible' invoice status that doesn't exist yet.
--
-- pause_collection_resumes_at is optional. When set, the cycle scan
-- auto-clears pause_collection at start of the period so that period
-- bills normally; when null, only an explicit DELETE clears it.
ALTER TABLE subscriptions
    ADD COLUMN pause_collection_behavior TEXT,
    ADD COLUMN pause_collection_resumes_at TIMESTAMPTZ,
    ADD CONSTRAINT subscriptions_pause_collection_behavior_check
        CHECK (pause_collection_behavior IS NULL OR pause_collection_behavior IN ('keep_as_draft'));

-- Partial index for the auto-resume scan: only rows currently paused
-- with a resumes_at timestamp need to be checked, which is tiny vs
-- the full table.
CREATE INDEX idx_subscriptions_pause_collection_resumes_at
    ON subscriptions (pause_collection_resumes_at)
    WHERE pause_collection_resumes_at IS NOT NULL;
