-- ADR-036 amendment — dunning final_action enum aligned with the
-- converging cross-platform terminal-action set verified 2026-05-16
-- against Stripe, Lago, Orb, and Recurly.
--
-- Old enum (singleton-policy era):
--   manual_review, pause, write_off_later
--
-- New enum (campaigns-model era):
--   manual_review        — leave open ("Keep active" in Stripe, default in Lago)
--   pause                — pause collection (semantics now match Stripe's
--                          pause_collection.behavior='keep_as_draft', i.e.
--                          cycle keeps drafting, NOT hard pause)
--   mark_uncollectible   — Stripe-standard term; replaces write_off_later
--   cancel_subscription  — Stripe-default terminal action; 3/4 platforms
--                          support it; previously missing in Velox
--
-- Backfill maps write_off_later → mark_uncollectible (same intent,
-- industry-standard spelling).

ALTER TABLE dunning_policies DROP CONSTRAINT IF EXISTS dunning_policies_final_action_check;

UPDATE dunning_policies SET final_action = 'mark_uncollectible' WHERE final_action = 'write_off_later';

ALTER TABLE dunning_policies
  ADD CONSTRAINT dunning_policies_final_action_check
  CHECK (final_action = ANY (ARRAY[
    'manual_review'::text,
    'pause'::text,
    'mark_uncollectible'::text,
    'cancel_subscription'::text
  ]));
