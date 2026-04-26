-- Real-time webhook event UI: track replay lineage across the event log.
--
-- The Week 6 dashboard (web-v2/src/pages/WebhookEvents.tsx) lets an
-- operator click "Replay" on any past event. Replays must be auditable —
-- a second click of Replay should not silently overwrite the first
-- attempt's timeline. We model this by writing a *new* webhook_events
-- row each time Replay is invoked, with replay_of_event_id pointing back
-- to the event the operator was looking at when they clicked Replay.
--
--   original event A  ──► deliveries (attempt 1, attempt 2 retry, …)
--                  └──► replay clone B (replay_of=A) ──► deliveries
--                  └──► replay clone C (replay_of=A) ──► deliveries
--
-- The list-deliveries endpoint walks the replay tree (the original plus
-- any replay_of=A rows) and returns a unified attempt timeline ordered
-- by attempted_at ASC, with attempt_no monotonic across the whole tree.
--
-- ON DELETE SET NULL because deleting an original event (cleanup,
-- compliance) should not cascade-delete the replay rows that still
-- carry their own deliveries.

ALTER TABLE webhook_events
    ADD COLUMN replay_of_event_id TEXT
        REFERENCES webhook_events(id) ON DELETE SET NULL;

-- Walk-the-tree query goes WHERE id = $1 OR replay_of_event_id = $1, so
-- a partial index on the replay-only side keeps the lookup cheap as the
-- event log grows.
CREATE INDEX idx_webhook_events_replay_of
    ON webhook_events (replay_of_event_id)
    WHERE replay_of_event_id IS NOT NULL;
