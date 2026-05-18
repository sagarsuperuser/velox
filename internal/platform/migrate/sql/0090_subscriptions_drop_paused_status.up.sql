-- Drop 'paused' from the subscriptions.status CHECK constraint.
--
-- The hard-pause API (POST /v1/subscriptions/:id/pause and /resume) was
-- removed in PR-8: the implementation's resume-behavior didn't match the
-- description ("freezes the cycle entirely" — but on resume the engine
-- caught up by billing every missed cycle, which is the opposite of a
-- freeze). The misleading UI option was removed in PR-6; PR-8 deletes
-- the backend surface (Service.Pause / Service.Resume / PauseAtomic /
-- ResumeAtomic / handler routes / SDK methods / EventSubscriptionPaused
-- + Resumed enum values).
--
-- With no path producing status='paused', the value is dead in the enum.
-- This migration removes it from the CHECK so the schema reflects the
-- runtime contract. Pre-launch: zero rows in 'paused' state — verified
-- below with a safety guard that errors loudly if any row IS in 'paused'
-- (so we don't silently lose data if this migration runs against a state
-- it can't represent).
--
-- pause_collection (Stripe-style soft pause, distinct mechanism, columns
-- pause_collection_behavior / pause_collection_resumes_at) is unaffected
-- — it doesn't use status='paused', it sets pause_collection_* on rows
-- with status='active'.
--
-- If a future design partner asks for true cycle-skip hard pause, the
-- right path is a fresh migration that adds the column shape outlined in
-- ADR-037's amendment (paused_at + resume_at + remaining_pause_cycles)
-- alongside re-adding 'paused' to the enum. ADR-037 captures the
-- industry-grade design (Chargebee / Recurly shape) so the work has a
-- starting point.

-- Safety guard: refuse to drop 'paused' from the enum if any row uses it.
-- Pre-launch this is moot, but cheap insurance against running this
-- migration against state we can't represent.
DO $$
DECLARE
    paused_count INT;
BEGIN
    SELECT COUNT(*) INTO paused_count FROM subscriptions WHERE status = 'paused';
    IF paused_count > 0 THEN
        RAISE EXCEPTION 'cannot drop ''paused'' from subscriptions.status enum: % row(s) still in paused state. Reconcile first (UPDATE subscriptions SET status=''canceled'' WHERE status=''paused'') or restore the hard-pause API surface.', paused_count;
    END IF;
END $$;

ALTER TABLE subscriptions
    DROP CONSTRAINT IF EXISTS subscriptions_status_check;

ALTER TABLE subscriptions
    ADD CONSTRAINT subscriptions_status_check
        CHECK (status IN ('draft', 'trialing', 'active', 'canceled', 'archived'));
