-- P11 (locked at plan time: feature-flags = DELETE). The subsystem is
-- inert: no runtime code path ever read a flag to gate behavior — the
-- gated features (auto-charge, webhooks, dunning, Stripe Tax, credit
-- auto-apply) all shipped hard-wired, and the flag rows sat as dead
-- config with their own API surface, store, and RLS carve-outs
-- (raw-pool store noted in the 2026-06 audit). Dead subsystems kept
-- green by tests cost every future census; delete rather than carry.
-- Order: overrides first (FK to feature_flags).
DROP TABLE IF EXISTS feature_flag_overrides;
DROP TABLE IF EXISTS feature_flags;
